package inventory

import (
	"context"
	"fmt"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/kubeshark/demo-apps/gen/inventory/v1"
	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

const slowDelayMs = 2000

// Flow is the gateway's side of the path: plaintext gRPC to the inventory
// service.
type Flow struct {
	target string
	client inventoryv1.InventoryServiceClient
	n      atomic.Uint64
}

func NewFlow(target string) (*Flow, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Flow{target: target, client: inventoryv1.NewInventoryServiceClient(conn)}, nil
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Stock lookup and reservation through the inventory gRPC API"
}
func (f *Flow) Path() string { return "gateway -> inventory (grpc) -> mongodb" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	n := f.n.Add(1)
	skus := Markers.Field("SKU")
	sku := skus[int(n-1)%len(skus)]
	orderID := fmt.Sprintf("%s%d", Markers.One("ORDER"), n)
	ctx = metadata.AppendToOutgoingContext(ctx, UserMetadata, Markers.One("USER"))
	t := &flowrun.Trace{}

	var delay uint32
	if v == flowrun.Slow {
		delay = slowDelayMs
	}
	var available int64
	err := t.Step("get stock", f.target, "grpc", func() (string, error) {
		resp, err := f.client.GetStock(ctx, &inventoryv1.GetStockRequest{Sku: sku, DelayMs: delay})
		if err != nil {
			return "", err
		}
		available = resp.GetAvailable()
		return fmt.Sprintf("%s: %d available at %s", resp.GetName(), available, resp.GetWarehouse()), nil
	})
	if err != nil {
		return t.Steps(), err
	}

	qty := int64(1)
	wantCode := codes.OK
	if v == flowrun.Error {
		qty = available + 1
		wantCode = codes.FailedPrecondition
	}
	err = t.Step("reserve", f.target, "grpc", func() (string, error) {
		resp, err := f.client.Reserve(ctx, &inventoryv1.ReserveRequest{Sku: sku, Quantity: qty, OrderId: orderID})
		if got := status.Code(err); got != wantCode {
			return "", fmt.Errorf("Reserve: code %s, want %s: %w", got, wantCode, err)
		}
		if err != nil {
			return fmt.Sprintf("%s as designed: %s", wantCode, status.Convert(err).Message()), nil
		}
		return fmt.Sprintf("order %s reserved %d, %d remaining", orderID, qty, resp.GetRemaining()), nil
	})
	return t.Steps(), err
}
