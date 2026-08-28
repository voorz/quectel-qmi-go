// Command qrtr-test drives every QMI service qmi-go wraps, end to end, over
// either the native QRTR transport (default) or classic QMUX (-qrtr=false),
// and prints a PASS/FAIL/SKIP report. It's meant for validating the QRTR
// transport on real hardware: CTL sync + version enumeration, then
// AllocateClientID -> one representative read-only query -> ReleaseClientID
// for each wrapped service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

type status int

const (
	statusPass status = iota
	statusFail
	statusSkip
)

func (s status) String() string {
	switch s {
	case statusPass:
		return "PASS"
	case statusSkip:
		return "SKIP"
	default:
		return "FAIL"
	}
}

type result struct {
	name   string
	status status
	detail string
}

func main() {
	useQRTR := flag.Bool("qrtr", true, "use native QRTR (AF_QIPCRTR) transport; -qrtr=false tests classic QMUX via -d for comparison")
	devicePath := flag.String("d", defaultQmiDevice(), "control device path, only used when -qrtr=false")
	verbose := flag.Bool("v", false, "print full response details, not just pass/fail")
	timeout := flag.Duration("timeout", 45*time.Second, "overall test timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	transportName := "QMUX"
	path := *devicePath
	if *useQRTR {
		transportName = "QRTR (AF_QIPCRTR)"
		path = ""
	}
	fmt.Printf("=== qmi-go transport test ===\n")
	fmt.Printf("transport : %s\n", transportName)
	if !*useQRTR {
		fmt.Printf("device    : %s\n", path)
	}
	fmt.Println()

	client, err := qmi.NewClientWithOptions(ctx, path, qmi.ClientOptions{
		UseQRTR:            *useQRTR,
		SyncOnOpen:         false, // we test Sync explicitly below
		QueryVersionOnOpen: false, // we test GetServiceVersions explicitly below
	})
	if err != nil {
		fmt.Printf("[FAIL] open transport: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()
	fmt.Println("[PASS] open transport")

	var results []result
	report := func(r result) {
		results = append(results, r)
		line := fmt.Sprintf("[%s] %s", r.status, r.name)
		if r.detail != "" && (*verbose || r.status != statusPass) {
			line += ": " + r.detail
		}
		fmt.Println(line)
	}

	// --- CTL layer -----------------------------------------------------
	report(runCTLSync(ctx, client))
	versions, verResult := runGetServiceVersions(ctx, client, *verbose)
	report(verResult)

	// --- Per-service tests ----------------------------------------------
	for _, r := range testDMS(ctx, client) {
		report(r)
	}
	for _, r := range testNAS(ctx, client) {
		report(r)
	}
	for _, r := range testWDS(ctx, client) {
		report(r)
	}
	for _, r := range testUIM(ctx, client) {
		report(r)
	}
	for _, r := range testWMS(ctx, client) {
		report(r)
	}
	for _, r := range testVOICE(ctx, client) {
		report(r)
	}
	for _, r := range testWDA(ctx, client) {
		report(r)
	}
	for _, r := range testIMS(ctx, client) {
		report(r)
	}
	for _, r := range testIMSA(ctx, client) {
		report(r)
	}
	for _, r := range testIMSP(ctx, client) {
		report(r)
	}

	printSummary(results, versions)

	for _, r := range results {
		if r.status == statusFail {
			os.Exit(1)
		}
	}
}

// ============================================================================
// CTL layer
// ============================================================================

func runCTLSync(ctx context.Context, client *qmi.Client) result {
	if err := client.Sync(ctx); err != nil {
		return result{name: "CTL Sync", status: statusFail, detail: err.Error()}
	}
	return result{name: "CTL Sync", status: statusPass}
}

func runGetServiceVersions(ctx context.Context, client *qmi.Client, verbose bool) ([]qmi.ServiceVersion, result) {
	versions, err := client.GetServiceVersions(ctx)
	if err != nil {
		return nil, result{name: "CTL GetVersionInfo", status: statusFail, detail: err.Error()}
	}
	names := make([]string, 0, len(versions))
	for _, v := range versions {
		names = append(names, fmt.Sprintf("%s(v%d.%d)", serviceName(uint16(v.ServiceType)), v.Major, v.Minor))
	}
	sort.Strings(names)
	detail := fmt.Sprintf("%d services", len(versions))
	if verbose {
		detail += ": " + strings.Join(names, ", ")
	}
	return versions, result{name: "CTL GetVersionInfo", status: statusPass, detail: detail}
}

// ============================================================================
// Per-service tests: allocate -> one safe read-only query -> release
// ============================================================================

func testDMS(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewDMSServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("DMS", err)}
	}
	defer svc.Close()
	out = append(out, ok("DMS AllocateClientID"))

	out = append(out, query("DMS GetDeviceSerialNumbers", func() (string, error) {
		info, err := svc.GetDeviceSerialNumbers(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("IMEI=%q ESN=%q MEID=%q", info.IMEI, info.ESN, info.MEID), nil
	}))
	out = append(out, query("DMS GetPINStatus", func() (string, error) {
		p, err := svc.GetPINStatus(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d", p.Status), nil
	}))
	out = append(out, query("DMS GetOperatingMode", func() (string, error) {
		m, err := svc.GetOperatingMode(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("mode=%d", m), nil
	}))
	return out
}

func testNAS(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewNASServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("NAS", err)}
	}
	defer svc.Close()
	out = append(out, ok("NAS AllocateClientID"))

	out = append(out, query("NAS GetServingSystem", func() (string, error) {
		s, err := svc.GetServingSystem(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("regState=%d mcc=%d mnc=%d", s.RegistrationState, s.MCC, s.MNC), nil
	}))
	out = append(out, query("NAS GetSignalStrength", func() (string, error) {
		s, err := svc.GetSignalStrength(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("rssi=%d", s.RSSI), nil
	}))
	out = append(out, query("NAS GetSysInfo", func() (string, error) {
		s, err := svc.GetSysInfo(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", s), nil
	}))
	return out
}

func testWDS(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewWDSServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("WDS", err)}
	}
	defer svc.Close()
	out = append(out, ok("WDS AllocateClientID"))

	out = append(out, query("WDS GetPacketServiceStatus", func() (string, error) {
		st, err := svc.GetPacketServiceStatus(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d", st), nil
	}))
	out = append(out, query("WDS GetProfileList", func() (string, error) {
		list, err := svc.GetProfileList(ctx, 0)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d profiles", len(list)), nil
	}))
	return out
}

func testUIM(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewUIMServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("UIM", err)}
	}
	defer svc.Close()
	out = append(out, ok("UIM AllocateClientID"))

	out = append(out, query("UIM GetCardStatusDetails", func() (string, error) {
		_, st, err := svc.GetCardStatusDetails(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("simStatus=%s", st.String()), nil
	}))
	out = append(out, query("UIM GetICCID", func() (string, error) {
		iccid, err := svc.GetICCID(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("iccid=%q", iccid), nil
	}))
	return out
}

func testWMS(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewWMSServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("WMS", err)}
	}
	defer svc.Close()
	out = append(out, ok("WMS AllocateClientID"))

	out = append(out, query("WMS GetSupportedMessages", func() (string, error) {
		modes, err := svc.GetSupportedMessages(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", modes), nil
	}))
	return out
}

func testVOICE(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewVOICEServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("VOICE", err)}
	}
	defer svc.Close()
	out = append(out, ok("VOICE AllocateClientID"))

	out = append(out, query("VOICE GetAllCallInfo", func() (string, error) {
		info, err := svc.GetAllCallInfo(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", info), nil
	}))
	return out
}

func testWDA(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewWDAServiceWithContext(ctx, client)
	if err != nil {
		return []result{allocFail("WDA", err)}
	}
	defer svc.Close()
	out = append(out, ok("WDA AllocateClientID"))

	out = append(out, query("WDA GetDataFormat", func() (string, error) {
		f, err := svc.GetDataFormat(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", f), nil
	}))
	return out
}

func testIMS(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewIMSService(client)
	if err != nil {
		return []result{allocFail("IMS", err)}
	}
	defer svc.Close()
	out = append(out, ok("IMS AllocateClientID"))

	out = append(out, query("IMS GetServicesEnabledSetting", func() (string, error) {
		s, err := svc.GetServicesEnabledSetting(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", s), nil
	}))
	return out
}

func testIMSA(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewIMSAService(client)
	if err != nil {
		return []result{allocFail("IMSA", err)}
	}
	defer svc.Close()
	out = append(out, ok("IMSA AllocateClientID"))

	out = append(out, query("IMSA GetIMSRegistrationStatus", func() (string, error) {
		s, err := svc.GetIMSRegistrationStatus(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", s), nil
	}))
	return out
}

func testIMSP(ctx context.Context, client *qmi.Client) []result {
	var out []result
	svc, err := qmi.NewIMSPService(client)
	if err != nil {
		return []result{allocFail("IMSP", err)}
	}
	defer svc.Close()
	out = append(out, ok("IMSP AllocateClientID"))

	out = append(out, query("IMSP GetEnablerState", func() (string, error) {
		s, err := svc.GetEnablerState(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("state=%d", s), nil
	}))
	return out
}

// ============================================================================
// Helpers
// ============================================================================

func ok(name string) result {
	return result{name: name, status: statusPass}
}

// allocFail classifies an AllocateClientID failure: a modem that legitimately
// doesn't offer a service (e.g. no IMS on a plain 4G module) reports
// ErrServiceNotSupported / a QMI "not supported" error, which is expected
// variance across hardware, not a transport bug -- report it as SKIP.
// Anything else (timeout, malformed response, transport-level failure) is a
// real FAIL.
func allocFail(service string, err error) result {
	return result{name: service + " AllocateClientID", status: classify(err), detail: err.Error()}
}

func query(name string, fn func() (string, error)) result {
	detail, err := fn()
	if err != nil {
		return result{name: name, status: classify(err), detail: err.Error()}
	}
	return result{name: name, status: statusPass, detail: detail}
}

func classify(err error) status {
	if err == nil {
		return statusPass
	}
	if errors.Is(err, qmi.ErrServiceNotSupported) {
		return statusSkip
	}
	var ns *qmi.NotSupportedError
	if errors.As(err, &ns) {
		return statusSkip
	}
	if qe := qmi.GetQMIError(err); qe != nil {
		switch qe.ErrorCode {
		case qmi.QMIErrNotSupported, qmi.QMIErrOpDeviceUnsupported, qmi.QMIErrInvalidQmiCmd:
			return statusSkip
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return statusFail
	}
	return statusFail
}

func printSummary(results []result, versions []qmi.ServiceVersion) {
	var pass, fail, skip int
	for _, r := range results {
		switch r.status {
		case statusPass:
			pass++
		case statusFail:
			fail++
		case statusSkip:
			skip++
		}
	}
	fmt.Println()
	fmt.Println("=== summary ===")
	fmt.Printf("modem reports %d QMI services supported\n", len(versions))
	fmt.Printf("pass=%d skip=%d fail=%d (total=%d)\n", pass, skip, fail, len(results))
	if fail > 0 {
		fmt.Println()
		fmt.Println("failed:")
		for _, r := range results {
			if r.status == statusFail {
				fmt.Printf("  - %s: %s\n", r.name, r.detail)
			}
		}
	}
}

// extendedServiceNames covers the full libqmi QmiService enum, including
// platform-internal services that qmi-go doesn't wrap (no service methods
// for them), purely so -v output is self-explanatory instead of raw hex.
// Values cross-checked against libqmi's qmi-enums.h and upstream
// tools/net/qrtr lookup.c's common_names[] table.
var extendedServiceNames = map[uint16]string{
	0x00:  "CTL",
	0x01:  "WDS",
	0x02:  "DMS",
	0x03:  "NAS",
	0x04:  "QOS",
	0x05:  "WMS",
	0x06:  "PDS",
	0x07:  "AUTH",
	0x08:  "AT",
	0x09:  "VOICE",
	0x0A:  "CAT2",
	0x0B:  "UIM",
	0x0C:  "PBM",
	0x0D:  "QCHAT",
	0x0E:  "RMTFS",
	0x0F:  "TEST",
	0x10:  "LOC",
	0x11:  "SAR",
	0x12:  "IMS",
	0x13:  "ADC",
	0x14:  "CSD",
	0x15:  "MFS",
	0x16:  "TIME",
	0x17:  "TS",
	0x18:  "TMD",
	0x19:  "SAP",
	0x1A:  "WDA",
	0x1B:  "TSYNC",
	0x1C:  "RFSA",
	0x1D:  "CSVT",
	0x1E:  "QCMAP",
	0x1F:  "IMSP",
	0x20:  "IMSVT",
	0x21:  "IMSA",
	0x22:  "COEX",
	0x24:  "PDC",
	0x26:  "STX",
	0x27:  "BIT",
	0x28:  "IMSRTP",
	0x29:  "RFRPE",
	0x2A:  "DSD",
	0x2B:  "SSCTL",
	0x2F:  "DPM",
	0x30:  "DFS", // QMI DFS service
	0x31:  "IPA",
	0x32:  "UIM_RMT",
	0x47:  "UIM_HTTP",
	0xE0:  "CAT1",
	0xE1:  "RMS",
	0xE2:  "OMA",
	0xE3:  "FOX",
	0xE6:  "FOTA",
	0xE7:  "GMS",
	0xE8:  "GAS",
	0xED:  "ATR",
	0x190: "SSC",    // Snapdragon Sensor Core
	0x302: "IMSDCM", // IMS data service
}

func serviceName(serviceID uint16) string {
	if name, ok := extendedServiceNames[serviceID]; ok {
		return name
	}
	return fmt.Sprintf("0x%04x", serviceID)
}

func defaultQmiDevice() string {
	candidates := []string{"/dev/cdc-wdm0", "/dev/cdc-wdm1", "/dev/cdc-wdm2", "/dev/cdc-wdm3"}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/dev/cdc-wdm0"
}
