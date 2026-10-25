import java.io.FileInputStream;
import java.io.InputStream;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyStore;
import java.security.cert.CertificateFactory;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.time.Duration;
import java.util.HashMap;
import java.util.Map;
import java.util.Properties;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;

// Calls a flow through the gateway over HTTPS and queries postgresql over TLS on an
// interval, so TLS from the JVM (JSSE) shows up next to Go and OpenSSL clients.
public class Client {
    public static void main(String[] args) throws Exception {
        Map<String, String> markers = markers(Path.of("/app/markers.tsv"));
        String agent = markers.get("AGENT-JAVA");
        String literal = markers.get("QUERY-JAVA");
        String ca = env("CA_FILE", "/etc/demo-apps/tls/ca.crt");
        String gateway = env("GATEWAY_URL", "https://demo-apps-gateway");
        String flow = env("FLOW", "catalog");
        Duration every = Duration.parse("PT" + env("INTERVAL", "15s").toUpperCase());

        HttpClient http = HttpClient.newBuilder()
                .sslContext(sslContext(ca))
                .connectTimeout(Duration.ofSeconds(5))
                .build();
        String jdbc = "jdbc:postgresql://" + env("POSTGRES_ADDR", "demo-apps-postgresql:5432") + "/" + env("POSTGRES_DB", "demo");
        Properties props = new Properties();
        props.setProperty("user", env("POSTGRES_USER", "demo"));
        props.setProperty("password", System.getenv("POSTGRES_PASSWORD"));
        props.setProperty("sslmode", "verify-full");
        props.setProperty("sslrootcert", ca);
        props.setProperty("ApplicationName", agent);

        while (true) {
            try {
                HttpRequest req = HttpRequest.newBuilder(URI.create(gateway + "/api/flows/" + flow + "/run?source=java"))
                        .header("X-Demo-Client", agent)
                        .timeout(Duration.ofSeconds(30))
                        .POST(HttpRequest.BodyPublishers.noBody())
                        .build();
                HttpResponse<String> resp = http.send(req, HttpResponse.BodyHandlers.ofString());
                log("https", "flow=" + flow + " status=" + resp.statusCode());
            } catch (Exception e) {
                log("https", "error=" + e);
            }
            // A new connection each time, so the TLS handshake and the authentication
            // exchange recur instead of happening once at startup.
            try (Connection conn = DriverManager.getConnection(jdbc, props);
                 PreparedStatement st = conn.prepareStatement("SELECT ?::text AS marker, version()")) {
                st.setString(1, literal);
                try (ResultSet rs = st.executeQuery()) {
                    rs.next();
                    log("postgresql", "selected=" + rs.getString(1));
                }
            } catch (Exception e) {
                log("postgresql", "error=" + e);
            }
            Thread.sleep(every.toMillis());
        }
    }

    static SSLContext sslContext(String caFile) throws Exception {
        KeyStore ks = KeyStore.getInstance(KeyStore.getDefaultType());
        ks.load(null, null);
        try (InputStream in = new FileInputStream(caFile)) {
            ks.setCertificateEntry("demo-apps", CertificateFactory.getInstance("X.509").generateCertificate(in));
        }
        TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ks);
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(null, tmf.getTrustManagers(), null);
        return ctx;
    }

    // Keyed by FIELD-REST of KSKEEP-<FLOW>-<FIELD>-<REST>.
    static Map<String, String> markers(Path file) throws Exception {
        Map<String, String> out = new HashMap<>();
        for (String line : Files.readAllLines(file)) {
            String[] cols = line.split("\t");
            if (cols.length >= 3) {
                String[] parts = cols[1].split("-", 4);
                if (parts.length == 4) {
                    out.put(parts[2] + "-" + parts[3], cols[1]);
                }
            }
        }
        return out;
    }

    static String env(String key, String def) {
        String v = System.getenv(key);
        return v == null || v.isEmpty() ? def : v;
    }

    static void log(String step, String msg) {
        System.out.println(java.time.Instant.now() + " " + step + " " + msg);
    }
}
