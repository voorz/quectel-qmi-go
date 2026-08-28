// Command dual-pdn-probe validates that two independent PDNs can be up at
// the same time on a modem, each bound to its own QMAP mux: by default a
// normal data PDN (mux A, IPv4) and a dedicated IMS PDN (mux B, IPv6), but
// every APN/mux/IP-family triple is a flag and either PDN can be skipped
// entirely by passing an empty APN.
//
// This is ims-probe's two-PDN sibling. ims-probe already proved a single
// IMS-APN PDN can come up on a QMAP mux; the one question it cannot answer
// is whether that PDN can coexist with a second, independent PDN on a
// second mux at the same time. That coexistence is the assumption the whole
// dual-APN IMS feature rests on. This probe is a throwaway hardware
// diagnostic to check it, not a production tool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run does all the work. Splitting main() into run() error lets every
// resource acquired along the way (QMI client, WDA/WDS clients, both QMAP
// muxes, both PDN handles) be released via defer on EVERY exit path,
// including failures: a bare log.Fatalf calls os.Exit and skips already
// registered defers, which would leak muxes (and possibly leave one or both
// PDNs up) on exactly the failure paths this probe cares most about.
func run() error {
	devicePath := flag.String("device", "/dev/cdc-wdm0", "QMI control device")
	iface := flag.String("iface", "wwan0", "master WWAN interface")

	apnA := flag.String("apn-a", "internet", "APN for PDN A (the normal data PDN by default). "+
		"An empty value skips PDN A entirely, so this probe can also be used to bring up just PDN B.")
	muxA := flag.Uint("mux-a", 1, "QMAP mux ID for PDN A")
	ipFamilyA := flag.Uint("ip-family-a", 4, "PDN A IP family: 4 or 6")

	apnB := flag.String("apn-b", "ims", "APN for PDN B (the dedicated IMS PDN by default). "+
		"An empty value skips PDN B entirely, so this probe can also be used to bring up just PDN A.")
	muxB := flag.Uint("mux-b", 2, "QMAP mux ID for PDN B")
	ipFamilyB := flag.Uint("ip-family-b", 6, "PDN B IP family: 4 or 6")

	epType := flag.Uint("ep-type", 2, "MuxBinding.EpType (QmiDataEndpointType: 1=HSIC, 2=HSUSB, 3=PCIe, "+
		"4=embedded), shared by both PDNs' binds. WDA Get Data Format does NOT report the endpoint in its "+
		"response -- Endpoint Info is an INPUT TLV only -- so this cannot be discovered and must be "+
		"supplied. 2 (HSUSB) matches the hardcoded value in pkg/manager/manager.go for USB-attached modems.")
	epIface := flag.Uint("ep-iface", 4, "WDA endpoint interface number used as MuxBinding.EpIfID, shared by "+
		"both PDNs' binds when binding their WDS clients to their QMAP muxes. This cannot be discovered "+
		"automatically either, so confirm it on real hardware and adjust this flag if BindMuxDataPort "+
		"fails for either PDN. 4 is the common USB interface number for Quectel QMI endpoints (EC20/EC25).")

	hold := flag.Duration("hold", 30*time.Second, "how long to hold both PDNs up")
	flag.Parse()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	client, err := qmi.NewClientWithOptions(context.Background(), *devicePath, qmi.ClientOptions{})
	if err != nil {
		return fmt.Errorf("open QMI client: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	wda, err := qmi.NewWDAService(client)
	if err != nil {
		return fmt.Errorf("WDA service: %w", err)
	}
	defer func() {
		if err := wda.Close(); err != nil {
			log.Printf("WARNING: release WDA client: %v", err)
		}
	}()

	fmt.Printf("PDN A: apn=%q mux=%d ip-family=%d\n", *apnA, *muxA, *ipFamilyA)
	fmt.Printf("PDN B: apn=%q mux=%d ip-family=%d\n", *apnB, *muxB, *ipFamilyB)

	details, err := wda.GetDataFormatDetails(ctx)
	if err != nil {
		return fmt.Errorf("get data format details: %w", err)
	}
	// NOTE: qmi-go's DataFormatDetails fields here are UlMaxDatagrams/
	// UlMaxSize, not EndpointType/EndpointID -- Get Data Format's response
	// carries no endpoint information at all (see the trap comment on
	// DataFormatDetails in pkg/qmi/wda.go). The bind below uses the
	// -ep-type / -ep-iface flags instead.
	fmt.Printf("current data format: link-protocol=%d ul-agg=%d dl-agg=%d (ul-agg-max-datagrams=%d ul-agg-max-size=%d)\n",
		details.LinkProtocol, details.UlDataAggregation, details.DlDataAggregation,
		details.UlMaxDatagrams, details.UlMaxSize)
	fmt.Printf("using ep-type=%d ep-iface=%d for both PDNs' MuxBinding (override if a bind fails on this modem)\n",
		*epType, *epIface)

	// QMAP multiplexing only works if the modem's data format actually has
	// the QMAP aggregation protocol enabled in both directions -- on real
	// hardware, BindMuxDataPort fails outright when it isn't. Force QMAP
	// here when needed, exactly as ims-probe does, and always restore
	// whatever was there before on exit via defer: SetDataFormat is
	// device-global, so leaving the modem in QMAP mode after this probe
	// would silently change behaviour for every other client of that modem,
	// not just this probe -- restoring it matters as much as releasing the
	// muxes below.
	if details.UlDataAggregation == qmi.DataAggregationProtocolQMAP && details.DlDataAggregation == qmi.DataAggregationProtocolQMAP {
		fmt.Println("modem data format aggregation is already QMAP; leaving it unchanged")
	} else {
		previous := qmi.DataFormat{
			LinkProtocol:      details.LinkProtocol,
			UlDataAggregation: details.UlDataAggregation,
			DlDataAggregation: details.DlDataAggregation,
		}
		fmt.Printf("modem data format aggregation is not QMAP (ul=%d dl=%d); switching to QMAP "+
			"(link-protocol=%d ul=%d dl=%d) so BindMuxDataPort can succeed for both PDNs\n",
			previous.UlDataAggregation, previous.DlDataAggregation,
			qmi.LinkProtocolIP, qmi.DataAggregationProtocolQMAP, qmi.DataAggregationProtocolQMAP)

		qmapFormat := qmi.DataFormat{
			LinkProtocol:      qmi.LinkProtocolIP,
			UlDataAggregation: qmi.DataAggregationProtocolQMAP,
			DlDataAggregation: qmi.DataAggregationProtocolQMAP,
			EndpointType:      uint32(*epType),
			InterfaceNumber:   uint32(*epIface),
		}
		if err := wda.SetDataFormat(ctx, qmapFormat); err != nil {
			return fmt.Errorf("set data format to QMAP: %w", err)
		}
		fmt.Println("modem data format switched to QMAP")

		defer func() {
			// Fresh context, not the setup ctx above: ctx carries an
			// absolute 2-minute deadline shared with both PDNs' setup and
			// the -hold sleep, and may already be expired by the time this
			// runs, which would silently no-op the restore and leave the
			// modem in QMAP mode.
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer restoreCancel()
			if err := wda.SetDataFormat(restoreCtx, previous); err != nil {
				log.Printf("WARNING: failed to restore modem data format (link-protocol=%d ul=%d dl=%d): %v",
					previous.LinkProtocol, previous.UlDataAggregation, previous.DlDataAggregation, err)
			} else {
				fmt.Printf("restored modem data format (link-protocol=%d ul=%d dl=%d)\n",
					previous.LinkProtocol, previous.UlDataAggregation, previous.DlDataAggregation)
			}
		}()
	}

	epTypeU32, epIfaceU32 := uint32(*epType), uint32(*epIface)

	// Bring up PDN A, then PDN B, each on its own mux with its own WDS
	// client. bringUpPDN never aborts run() on failure -- it returns a
	// result value instead -- so that a failure on PDN A does not prevent
	// attempting PDN B (knowing whether B works alone is useful data too),
	// and a failure on PDN B can never skip PDN A's teardown: defer cleanupA
	// below is registered unconditionally, before PDN B is even attempted.
	cfgA := pdnConfig{label: "A", apn: *apnA, muxID: uint8(*muxA), ipFamily: uint8(*ipFamilyA)}
	resA, cleanupA := bringUpPDN(ctx, client, *iface, epTypeU32, epIfaceU32, cfgA)
	defer cleanupA()

	cfgB := pdnConfig{label: "B", apn: *apnB, muxID: uint8(*muxB), ipFamily: uint8(*ipFamilyB)}
	resB, cleanupB := bringUpPDN(ctx, client, *iface, epTypeU32, epIfaceU32, cfgB)
	defer cleanupB()

	fmt.Println()
	fmt.Println("=== per-PDN report ===")
	reportPDN(resA)
	reportPDN(resB)

	fmt.Println()
	printVerdict(resA, resB)

	fmt.Println()
	fmt.Printf("holding both PDNs for %s — inspect from another shell\n", *hold)
	select {
	case <-time.After(*hold):
	case <-sigCtx.Done():
		fmt.Println("interrupt received, cleaning up — please wait (do not press Ctrl-C again)")
	}
	return nil
}

// pdnConfig holds one PDN's flag-derived parameters.
type pdnConfig struct {
	label    string // "A" or "B" -- prefixes every log line and error for this PDN
	apn      string
	muxID    uint8
	ipFamily uint8 // raw flag value, 4 or 6; convert to the QMI wire value with ipFamilyWire
}

// pdnResult captures what happened bringing up one PDN, for the per-PDN
// report and the coexistence verdict.
type pdnResult struct {
	cfg pdnConfig

	skipped bool // cfg.apn was empty -- this PDN was intentionally not attempted

	muxIface string

	up       bool // StartNetworkInterface succeeded; handle is meaningful
	handle   uint32
	settings *qmi.RuntimeSettings // non-nil once GetRuntimeSettings succeeds

	stage string // step that failed: "mux", "wds-open", "bind", "start-network", "runtime-settings"
	err   error  // first error that stopped this PDN's setup, if any
}

// bringUpPDN creates a QMAP mux, opens a dedicated WDS client, binds it to
// the mux, starts the network on cfg.apn, and reads back runtime settings.
//
// It always returns a non-nil cleanup func, however far setup got, and never
// returns an error that would abort run(): failures are recorded on the
// returned result instead. The caller MUST `defer` the returned cleanup
// immediately -- registering the defer here, inside bringUpPDN, would run it
// as soon as bringUpPDN returns (i.e. before the other PDN is even
// attempted, and long before the -hold sleep), which is the opposite of
// what this probe needs: both PDNs must stay up until run() itself returns,
// so both defers must live in run()'s own frame, same as every resource
// ims-probe acquires.
func bringUpPDN(ctx context.Context, client *qmi.Client, masterIface string, epType, epIface uint32, cfg pdnConfig) (*pdnResult, func()) {
	res := &pdnResult{cfg: cfg}
	cleanup := func() {}

	if cfg.apn == "" {
		res.skipped = true
		fmt.Printf("PDN %s: skipped (-apn-%s is empty)\n", cfg.label, lowerLabel(cfg.label))
		return res, cleanup
	}

	muxIface, err := netcfg.AddQMAPMux(masterIface, cfg.muxID)
	if err != nil {
		res.stage = "mux"
		res.err = fmt.Errorf("PDN %s: create QMAP mux %d failed: %w", cfg.label, cfg.muxID, err)
		return res, cleanup
	}
	res.muxIface = muxIface
	fmt.Printf("PDN %s: mux interface %s (mux id %d)\n", cfg.label, muxIface, cfg.muxID)
	cleanup = func() {
		if err := netcfg.DelQMAPMux(masterIface, cfg.muxID); err != nil {
			log.Printf("WARNING: PDN %s: leaked mux %d: %v", cfg.label, cfg.muxID, err)
		}
	}

	wds, err := qmi.NewWDSService(client)
	if err != nil {
		res.stage = "wds-open"
		res.err = fmt.Errorf("PDN %s: WDS service: %w", cfg.label, err)
		return res, cleanup
	}
	deleteMux := cleanup
	cleanup = func() {
		if err := wds.Close(); err != nil {
			log.Printf("WARNING: PDN %s: release WDS client: %v", cfg.label, err)
		}
		deleteMux()
	}

	binding := qmi.MuxBinding{
		EpType:     epType,
		EpIfID:     epIface,
		MuxID:      cfg.muxID,
		ClientType: 0x01, // QMI_WDS_CLIENT_TYPE_TETHERED
	}
	if err := wds.BindMuxDataPort(ctx, binding); err != nil {
		res.stage = "bind"
		res.err = fmt.Errorf("PDN %s: bind mux data port failed: %w", cfg.label, err)
		return res, cleanup
	}
	fmt.Printf("PDN %s: bound WDS client to mux %d\n", cfg.label, cfg.muxID)

	family := ipFamilyWire(cfg.ipFamily)
	handle, err := wds.StartNetworkInterface(ctx, cfg.apn, "", "", 0, family)
	if err != nil {
		res.stage = "start-network"
		res.err = fmt.Errorf("PDN %s: start network on APN %q failed: %w", cfg.label, cfg.apn, err)
		return res, cleanup
	}
	res.up = true
	res.handle = handle
	fmt.Printf("PDN %s: up, handle=%d\n", cfg.label, handle)
	closeWDSAndDeleteMux := cleanup
	cleanup = func() {
		// Fresh context, not the setup ctx above: same rationale as the
		// data format restore -- ctx shares its 2-minute deadline with both
		// PDNs' setup and the -hold sleep and may already be expired by the
		// time this runs, which would silently no-op the stop and leak the
		// PDN on the modem -- the exact hazard this probe exists to catch.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := wds.StopNetworkInterface(stopCtx, handle); err != nil {
			log.Printf("WARNING: PDN %s: stop network: %v", cfg.label, err)
		}
		closeWDSAndDeleteMux()
	}

	settings, err := wds.GetRuntimeSettings(ctx, family)
	if err != nil {
		res.stage = "runtime-settings"
		res.err = fmt.Errorf("PDN %s: get runtime settings failed: %w", cfg.label, err)
		return res, cleanup
	}
	res.settings = settings

	return res, cleanup
}

// reportPDN prints the per-PDN summary required of this probe: whether it
// came up, its handle, its IPv4/IPv6 address, MTU, IMCN flag, and P-CSCF
// addresses/domains.
func reportPDN(res *pdnResult) {
	label := res.cfg.label

	if res.skipped {
		fmt.Printf("PDN %s: skipped (empty APN)\n", label)
		return
	}

	if !res.up {
		fmt.Printf("PDN %s: DID NOT COME UP (failed at stage %q): %v\n", label, res.stage, res.err)
		if detail := describeQMIFailure(res.err); detail != "" {
			fmt.Printf("PDN %s: %s\n", label, detail)
		}
		return
	}

	fmt.Printf("PDN %s: up on APN %q, mux %d (%s), handle=%d\n", label, res.cfg.apn, res.cfg.muxID, res.muxIface, res.handle)
	if res.settings == nil {
		fmt.Printf("PDN %s: up but get runtime settings failed (stage %q): %v\n", label, res.stage, res.err)
		if detail := describeQMIFailure(res.err); detail != "" {
			fmt.Printf("PDN %s: %s\n", label, detail)
		}
		return
	}

	s := res.settings
	fmt.Printf("PDN %s: IPv4=%v IPv6=%v MTU=%d\n", label, s.IPv4Address, s.IPv6Address, s.MTU)
	fmt.Printf("PDN %s: IMCN flag: %v\n", label, s.IMCN)
	fmt.Printf("PDN %s: P-CSCF addresses: %v\n", label, s.PCSCFv4)
	fmt.Printf("PDN %s: P-CSCF domains: %v\n", label, s.PCSCFDomains)
}

// printVerdict prints the explicit coexistence verdict this probe exists to
// produce: whether both PDNs came up at the same time on distinct
// addresses, or, if not, exactly where coexistence broke down.
func printVerdict(a, b *pdnResult) {
	fmt.Println("=== coexistence verdict ===")

	switch {
	case a.skipped && b.skipped:
		fmt.Println("both PDNs were skipped (empty APN on both) -- nothing to verify")
		return
	case a.skipped:
		fmt.Println("PDN A was skipped; only PDN B was requested, so coexistence was not exercised (single-PDN run)")
		reportSingleOutcome(b)
		return
	case b.skipped:
		fmt.Println("PDN B was skipped; only PDN A was requested, so coexistence was not exercised (single-PDN run)")
		reportSingleOutcome(a)
		return
	}

	switch {
	case a.up && b.up:
		if addressesDistinct(a.settings, b.settings) {
			fmt.Printf("COEXISTENCE CONFIRMED: PDN A (mux %d) and PDN B (mux %d) are both up at the same "+
				"time on distinct addresses -- A: %s  B: %s\n",
				a.cfg.muxID, b.cfg.muxID, fmtIPs(a.settings), fmtIPs(b.settings))
		} else {
			fmt.Printf("COEXISTENCE UNCERTAIN: both PDN A (handle=%d) and PDN B (handle=%d) report up, "+
				"but distinct addresses could not be confirmed between them -- A: %s  B: %s\n",
				a.handle, b.handle, fmtIPs(a.settings), fmtIPs(b.settings))
		}
	case a.up && !b.up:
		fmt.Printf("COEXISTENCE FAILED: PDN A is up alone (mux %d, handle=%d); PDN B failed to come up "+
			"while A was active, at stage %q: %v\n", a.cfg.muxID, a.handle, b.stage, b.err)
		if detail := describeQMIFailure(b.err); detail != "" {
			fmt.Printf("PDN B failure detail: %s\n", detail)
		}
	case !a.up && b.up:
		fmt.Printf("COEXISTENCE NOT DEMONSTRATED: PDN A failed before B was attempted, at stage %q: %v -- "+
			"but PDN B came up on its own (mux %d, handle=%d), so B works in isolation even though "+
			"simultaneous coexistence with A was never established\n", a.stage, a.err, b.cfg.muxID, b.handle)
		if detail := describeQMIFailure(a.err); detail != "" {
			fmt.Printf("PDN A failure detail: %s\n", detail)
		}
	default:
		fmt.Printf("COEXISTENCE FAILED: neither PDN came up -- PDN A at stage %q: %v; PDN B at stage %q: %v\n",
			a.stage, a.err, b.stage, b.err)
		if detail := describeQMIFailure(a.err); detail != "" {
			fmt.Printf("PDN A failure detail: %s\n", detail)
		}
		if detail := describeQMIFailure(b.err); detail != "" {
			fmt.Printf("PDN B failure detail: %s\n", detail)
		}
	}
}

// reportSingleOutcome prints the one-PDN-requested outcome used by
// printVerdict when the other PDN was skipped entirely.
func reportSingleOutcome(r *pdnResult) {
	if r.up {
		fmt.Printf("PDN %s is up alone (mux %d, handle=%d)\n", r.cfg.label, r.cfg.muxID, r.handle)
		return
	}
	fmt.Printf("PDN %s failed at stage %q: %v\n", r.cfg.label, r.stage, r.err)
	if detail := describeQMIFailure(r.err); detail != "" {
		fmt.Printf("PDN %s failure detail: %s\n", r.cfg.label, detail)
	}
}

// addressesDistinct reports whether a and b describe genuinely separate
// endpoints. Both sides must have reported at least one address at all
// (nil settings, or a PDN with no address in any family yet, cannot support
// a distinctness claim); beyond that, a shared address family only counts
// against distinctness if both sides actually agree on its value -- e.g. an
// IPv4-only PDN and an IPv6-only PDN are trivially distinct.
func addressesDistinct(a, b *qmi.RuntimeSettings) bool {
	if a == nil || b == nil {
		return false
	}
	aHasAddr := a.IPv4Address != nil || a.IPv6Address != nil
	bHasAddr := b.IPv4Address != nil || b.IPv6Address != nil
	if !aHasAddr || !bHasAddr {
		return false
	}
	if a.IPv4Address != nil && b.IPv4Address != nil && a.IPv4Address.Equal(b.IPv4Address) {
		return false
	}
	if a.IPv6Address != nil && b.IPv6Address != nil && a.IPv6Address.Equal(b.IPv6Address) {
		return false
	}
	return true
}

// fmtIPs renders a PDN's addresses for the verdict line, tolerating a nil
// settings value (up succeeded but the runtime-settings query failed).
func fmtIPs(s *qmi.RuntimeSettings) string {
	if s == nil {
		return "unknown (runtime settings unavailable)"
	}
	return fmt.Sprintf("IPv4=%v IPv6=%v", s.IPv4Address, s.IPv6Address)
}

// describeQMIFailure renders a QMI-level failure the way an operator needs
// to read it: the QMI error code first -- this distinguishes "the modem
// refused the request" (bad argument, wrong state, unsupported command...)
// from a network-side rejection -- then, only when the modem supplied one,
// the call-end reason type/code that StartNetworkInterface attaches on
// failure (e.g. ESM cause 51 "PDN type IPv6 only allowed" arrives this way).
func describeQMIFailure(err error) string {
	if err == nil {
		return ""
	}

	desc := fmt.Sprintf("error: %v", err)
	if qe := qmi.GetQMIError(err); qe != nil {
		desc = fmt.Sprintf("QMI error 0x%04x (service=0x%04x msg=0x%04x result=0x%04x)",
			qe.ErrorCode, qe.Service, qe.MessageID, qe.Result)
	}

	var sne *qmi.StartNetworkError
	if errors.As(err, &sne) && sne.Reason != nil {
		desc += fmt.Sprintf("; call-end reason type=%d code=%d", sne.Reason.Type, sne.Reason.Code)
	}

	return desc
}

// ipFamilyWire clamps a raw -ip-family-* flag value to the two values WDS
// actually accepts (4 or 6), defaulting to IPv4 for anything else -- the
// same defensive fallback ims-probe uses.
func ipFamilyWire(raw uint8) uint8 {
	if raw == 6 {
		return 6
	}
	return 4
}

// lowerLabel maps a PDN label ("A"/"B") to the flag-name suffix used by
// -apn-a/-apn-b, purely for the skip message above.
func lowerLabel(label string) string {
	if label == "A" {
		return "a"
	}
	return "b"
}
