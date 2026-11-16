package chartcontract

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSnapshotSeed(t *testing.T) {
	metadataBytes, err := os.ReadFile("../../deploy/chart/files/contracts/workload.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var metadata contract
	if err := yaml.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join("../../deploy/chart", metadata.SnapshotSeed.Path))
	if err != nil {
		t.Fatal(err)
	}
	testSnapshotSeed(t, metadata, seed)
}

type seedConnection struct {
	stage   int
	next    [2]uint32
	fin     [2]bool
	payload [2][]byte
}

func testSnapshotSeed(t *testing.T, metadata contract, seed []byte) {
	t.Helper()
	if metadata.SnapshotSeed.Path != "files/seeds/snapshot.pcap" || metadata.SnapshotSeed.Format != "pcap-dlt-raw" {
		t.Fatalf("unsupported snapshot seed identity: %+v", metadata.SnapshotSeed)
	}
	if len(seed) < 24 || len(seed) > 512*1024 {
		t.Fatalf("seed size = %d, want complete PCAP <=512 KiB", len(seed))
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(seed)); metadata.SnapshotSeed.SHA256 != digest {
		t.Fatalf("metadata SHA256 = %s, actual = %s", metadata.SnapshotSeed.SHA256, digest)
	}
	le, be := binary.LittleEndian, binary.BigEndian
	if le.Uint32(seed) != 0xa1b2c3d4 || le.Uint16(seed[4:]) != 2 || le.Uint16(seed[6:]) != 4 ||
		le.Uint32(seed[8:]) != 0 || le.Uint32(seed[12:]) != 0 || le.Uint32(seed[16:]) != 65535 || le.Uint32(seed[20:]) != 101 {
		t.Fatal("seed must be microsecond libpcap 2.4 with DLT_RAW (101), no auxiliary metadata")
	}
	type key struct {
		client, server [4]byte
		port           uint16
	}
	connections := map[key]*seedConnection{}
	var lastTimestamp uint64
	packets := 0
	for offset := 24; offset < len(seed); {
		if len(seed)-offset < 16 {
			t.Fatal("truncated PCAP record header")
		}
		header := seed[offset : offset+16]
		size := int(le.Uint32(header[8:]))
		seconds, micros := le.Uint32(header), le.Uint32(header[4:])
		timestamp := uint64(seconds)*1000000 + uint64(micros)
		if seconds < 1704067200 || seconds >= 1704153600 || micros >= 1000000 || (packets > 0 && timestamp <= lastTimestamp) {
			t.Fatal("seed timestamps must increase within the fixed synthetic day 2024-01-01 UTC")
		}
		lastTimestamp = timestamp
		offset += 16
		if size < 40 || size > 65535 || size > len(seed)-offset || le.Uint32(header[12:]) != uint32(size) {
			t.Fatal("truncated, empty or clipped PCAP packet")
		}
		packet := seed[offset : offset+size]
		offset += size
		packets++
		if packet[0] != 0x45 || be.Uint16(packet[2:]) != uint16(size) || be.Uint16(packet[6:]) != 0x4000 || packet[9] != 6 || seedChecksum(packet[:20]) != 0 {
			t.Fatal("expected complete unfragmented IPv4/TCP with valid IPv4 checksum and no IP options")
		}
		var src, dst [4]byte
		copy(src[:], packet[12:16])
		copy(dst[:], packet[16:20])
		if !documentationIP(src) || !documentationIP(dst) {
			t.Fatal("seed contains an address outside RFC 5737 documentation networks")
		}
		tcp := packet[20:]
		if tcp[12] != 0x50 || be.Uint16(tcp[18:]) != 0 {
			t.Fatal("TCP options, reserved bits and urgent data are not part of the synthetic seed")
		}
		pseudo := append([]byte{}, packet[12:20]...)
		pseudo = append(pseudo, 0, 6, byte(len(tcp)>>8), byte(len(tcp)))
		if seedChecksum(append(pseudo, tcp...)) != 0 {
			t.Fatal("invalid TCP checksum")
		}
		sport, dport := be.Uint16(tcp), be.Uint16(tcp[2:])
		direction := 0
		k := key{client: src, server: dst, port: sport}
		if sport == 8080 {
			direction = 1
			k = key{client: dst, server: src, port: dport}
		} else if dport != 8080 {
			t.Fatal("seed server port must be lower than the ephemeral client port for rawcapture flow extraction")
		}
		if k.port < 32768 {
			t.Fatal("seed client port must be ephemeral")
		}
		flow := connections[k]
		if flow == nil {
			flow = &seedConnection{}
			connections[k] = flow
		}
		seq, ack, flags, data := be.Uint32(tcp[4:]), be.Uint32(tcp[8:]), tcp[13], tcp[20:]
		switch flow.stage {
		case 0:
			if direction != 0 || flags != 0x02 || ack != 0 || len(data) != 0 {
				t.Fatal("connection must start with client SYN")
			}
			flow.next[0], flow.stage = seq+1, 1
		case 1:
			if direction != 1 || flags != 0x12 || ack != flow.next[0] || len(data) != 0 {
				t.Fatal("connection must answer with server SYN/ACK")
			}
			flow.next[1], flow.stage = seq+1, 2
		default:
			if flow.stage == 4 || seq != flow.next[direction] || ack != flow.next[1-direction] {
				t.Fatal("TCP sequence/acknowledgment discontinuity or packet after teardown")
			}
			switch {
			case flow.stage == 2:
				if direction != 0 || flags != 0x10 || len(data) != 0 {
					t.Fatal("incomplete TCP handshake")
				}
				flow.stage = 3
			case flow.fin[0] && flow.fin[1]:
				if direction != 0 || flags != 0x10 || len(data) != 0 {
					t.Fatal("teardown must end with client ACK")
				}
				flow.stage = 4
			case flags == 0x11 && len(data) == 0 && !flow.fin[direction]:
				if direction == 1 && !flow.fin[0] {
					t.Fatal("server FIN precedes client FIN")
				}
				flow.fin[direction] = true
				flow.next[direction]++
			case flags == 0x18 && len(data) > 0 && !flow.fin[0] && !flow.fin[1]:
				if direction == 0 && len(flow.payload[1]) > 0 {
					t.Fatal("request data follows the response")
				}
				flow.payload[direction] = append(flow.payload[direction], data...)
				flow.next[direction] += uint32(len(data))
			case flags != 0x10 || len(data) != 0:
				t.Fatal("unexpected TCP flags or payload")
			}
		}
	}
	if len(connections) < 20 || packets < 200 {
		t.Fatalf("insufficient replay variation: %d connections, %d packets", len(connections), packets)
	}
	methods, statuses, requests, responses := map[string]bool{}, map[int]bool{}, map[[32]byte]bool{}, map[[32]byte]bool{}
	for _, flow := range connections {
		if flow.stage != 4 {
			t.Fatal("connection is missing complete TCP teardown")
		}
		method, status := testSeedHTTP(t, flow.payload[0], flow.payload[1])
		methods[method], statuses[status] = true, true
		requests[sha256.Sum256(flow.payload[0])], responses[sha256.Sum256(flow.payload[1])] = true, true
	}
	if len(methods) < 4 || len(statuses) < 4 || len(requests) < 20 || len(responses) < 20 {
		t.Fatal("seed must vary HTTP methods, success/error statuses and request/response payloads")
	}
}

func documentationIP(ip [4]byte) bool {
	address := netip.AddrFrom4(ip)
	for _, prefix := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		if netip.MustParsePrefix(prefix).Contains(address) {
			return true
		}
	}
	return false
}

func seedChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

var seedPath = regexp.MustCompile(`^/synthetic/(catalog|orders|stock|missing|reject)/([0-9]{3})$`)
var seedSKU = regexp.MustCompile(`^synthetic-item-[0-9]{3}$`)

func testSeedHTTP(t *testing.T, requestBytes, responseBytes []byte) (string, int) {
	t.Helper()
	requestReader := bufio.NewReader(bytes.NewReader(requestBytes))
	request, err := http.ReadRequest(requestReader)
	if err != nil {
		t.Fatalf("reassembled request: %v", err)
	}
	path := seedPath.FindStringSubmatch(request.RequestURI)
	if request.Proto != "HTTP/1.1" || request.Host != "snapshot.demo.invalid" || len(path) != 3 || !request.Close {
		t.Fatal("HTTP request must use only synthetic paths and .invalid host, with connection close")
	}
	sequence, _ := strconv.Atoi(path[2])
	operation := path[1]
	methods := map[string]string{"catalog": "GET", "orders": "POST", "stock": "PUT", "missing": "DELETE", "reject": "POST"}
	statuses := map[string]int{"catalog": 200, "orders": 201, "stock": 200, "missing": 404, "reject": 422}
	results := map[string]string{"catalog": "synthetic-ok", "orders": "synthetic-created", "stock": "synthetic-updated", "missing": "synthetic-not-found", "reject": "synthetic-invalid-quantity"}
	if request.Method != methods[operation] {
		t.Fatal("unexpected synthetic request method")
	}
	requestBody := readSeedBody(t, request.Body, request.ContentLength, requestReader)
	testSeedHeaders(t, request.Header, map[string]string{
		"User-Agent": "demo-apps-synthetic-seed/1", "Accept": "application/json", "Connection": "close",
		"Content-Length": strconv.Itoa(len(requestBody)), "Content-Type": "application/json",
	})
	if request.Method == "POST" || request.Method == "PUT" {
		testSeedJSON(t, requestBody, sequence, operation, "")
	} else if len(requestBody) != 0 {
		t.Fatal("unexpected request body")
	}
	responseReader := bufio.NewReader(bytes.NewReader(responseBytes))
	response, err := http.ReadResponse(responseReader, request)
	if err != nil {
		t.Fatalf("reassembled response: %v", err)
	}
	if response.Proto != "HTTP/1.1" || response.StatusCode != statuses[operation] || !response.Close {
		t.Fatal("unexpected synthetic response status or framing")
	}
	responseBody := readSeedBody(t, response.Body, response.ContentLength, responseReader)
	testSeedHeaders(t, response.Header, map[string]string{
		"Server": "demo-apps-synthetic-seed/1", "Content-Type": "application/json", "Connection": "close",
		"Content-Length": strconv.Itoa(len(responseBody)),
	})
	testSeedJSON(t, responseBody, sequence, operation, results[operation])
	return request.Method, response.StatusCode
}

func readSeedBody(t *testing.T, body io.ReadCloser, length int64, reader *bufio.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != length {
		t.Fatal("HTTP content length does not match the complete body")
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("unaccounted data after HTTP body")
	}
	return data
}

func testSeedHeaders(t *testing.T, headers http.Header, allowed map[string]string) {
	t.Helper()
	for name, values := range headers {
		value, ok := allowed[name]
		if !ok || len(values) != 1 || values[0] != value {
			t.Fatalf("non-synthetic or unexpected HTTP header %q", name)
		}
	}
}

func testSeedJSON(t *testing.T, body []byte, sequence int, operation, result string) {
	t.Helper()
	var value struct {
		Synthetic bool   `json:"synthetic"`
		Sequence  int    `json:"sequence"`
		Operation string `json:"operation"`
		Result    string `json:"result"`
		Items     []struct {
			SKU      string `json:"sku"`
			Quantity int    `json:"quantity"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("synthetic JSON schema: %v", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatal("unaccounted data after synthetic JSON")
	}
	if !value.Synthetic || sequence < 1 || sequence > 25 || value.Sequence != sequence || value.Operation != operation || value.Result != result || len(value.Items) < 1 || len(value.Items) > 12 {
		t.Fatal("body must contain only the documented synthetic data schema")
	}
	for _, item := range value.Items {
		if !seedSKU.MatchString(item.SKU) || item.Quantity < 1 || item.Quantity > 9 {
			t.Fatal("body contains a non-synthetic SKU or quantity")
		}
	}
}
