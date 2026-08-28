// Command ims-probe validates that a modem without native VoLTE can still
// establish a dedicated IMS APN PDN over a QMAP mux, and reports whether the
// network delivers P-CSCF addresses and the IMCN flag.
//
// This is a throwaway hardware probe, not a production tool: it exists only
// to answer the go/no-go question for assumptions A1 (an IMS-APN PDN can
// coexist with the default data PDN over a QMAP mux) and A2 (the network
// will hand out an ims APN PDN to a modem that does not advertise native
// VoLTE support). Record the result of running this against real hardware in
// whatever verification log this project keeps for hardware probes.
package main

import (
	"context"
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
// resource acquired along the way (QMI client, WDA/WDS clients, the QMAP
// mux, the IMS PDN handle) be released via defer on EVERY exit path,
// including failures: a bare log.Fatalf calls os.Exit and skips already
// registered defers, which would leak the QMAP mux (and possibly leave the
// IMS PDN up) on exactly the failure paths this probe cares most about.
func run() error {
	devicePath := flag.String("device", "/dev/cdc-wdm0", "QMI control device")
	useProxy := flag.Bool("proxy", false, "use qmi-proxy instead of opening the QMI control device directly")
	proxyPath := flag.String("proxy-path", "", "qmi-proxy socket path (default: qmi-proxy)")
	proxyExecutable := flag.String("proxy-executable", "", "qmi-proxy executable used only when the socket is unavailable")
	iface := flag.String("iface", "wwan0", "master WWAN interface")
	apn := flag.String("apn", "ims", "APN to activate")
	muxID := flag.Uint("mux", 1, "QMAP mux ID for the IMS PDN")
	ipFamily := flag.Uint("ip-family", 4, "4 or 6")
	hold := flag.Duration("hold", 30*time.Second, "how long to hold the PDN up")
	epType := flag.Uint("ep-type", 2, "MuxBinding.EpType (QmiDataEndpointType: 1=HSIC, 2=HSUSB, 3=PCIe, "+
		"4=embedded). WDA Get Data Format does NOT report the endpoint in its response -- Endpoint Info is "+
		"an INPUT TLV only -- so this cannot be discovered and must be supplied. 2 (HSUSB) matches the "+
		"hardcoded value in pkg/manager/manager.go for USB-attached modems.")
	epIface := flag.Uint("ep-iface", 4, "WDA endpoint interface number used as MuxBinding.EpIfID when "+
		"binding the WDS client to the QMAP mux. The WDA Get Data Format response on this modem only "+
		"report the endpoint at all (Endpoint Info is an INPUT-only TLV), so this cannot be discovered "+
		"automatically and must be confirmed on real hardware, adjusting this flag if "+
		"BindMuxDataPort fails. 4 is the common USB interface number for Quectel QMI endpoints (EC20/EC25).")
	skipQMAPSetup := flag.Bool("skip-qmap-setup", false, "skip forcing the modem's uplink/downlink "+
		"data format aggregation to QMAP before binding the mux. QMAP multiplexing cannot work unless "+
		"both directions already report the QMAP aggregation protocol, so by default this probe checks "+
		"and, if needed, sets it -- saving the previous data format and restoring it on exit. Set this "+
		"only when the modem is already known-good (e.g. confirmed via wda-tool, or left in QMAP mode by "+
		"a prior run of this probe) and you want to skip the extra SetDataFormat round trip; if "+
		"aggregation genuinely is not QMAP, this flag will make BindMuxDataPort fail the same way it did "+
		"before this check existed.")
	flag.Parse()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	client, err := qmi.NewClientWithOptions(context.Background(), *devicePath, qmi.ClientOptions{
		UseProxy:           *useProxy,
		ProxyPath:          *proxyPath,
		ProxyExecutable:    *proxyExecutable,
		ProxyFallbackToRaw: false,
	})
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

	details, err := wda.GetDataFormatDetails(ctx)
	if err != nil {
		return fmt.Errorf("get data format details: %w", err)
	}
	// NOTE: qmi-go's DataFormatDetails fields here are UlMaxDatagrams/
	// UlMaxSize, not EndpointType/EndpointID. Per libqmi (data/qmi-service-
	// wda.json, Get Data Format), output TLV 0x17 is "Uplink Data
	// Aggregation Max Datagrams" and 0x18 is "Uplink Data Aggregation Max
	// Size" -- neither carries endpoint information. Endpoint Info exists
	// only as an INPUT TLV (and at a different TLV number on Set Data
	// Format), so the endpoint cannot be discovered from this response at
	// all; the bind below uses the -ep-type / -ep-iface flags instead.
	fmt.Printf("link-protocol=%d ul-agg-max-datagrams=%d ul-agg-max-size=%d\n",
		details.LinkProtocol, details.UlMaxDatagrams, details.UlMaxSize)
	fmt.Printf("using ep-type=%d ep-iface=%d for MuxBinding (override if the bind fails on this modem)\n",
		*epType, *epIface)

	// QMAP multiplexing only works if the modem's data format actually has
	// the QMAP aggregation protocol enabled in both directions. On real
	// hardware, BindMuxDataPort fails outright when it isn't (EC25: QMI
	// error 0x0030 invalid argument; EM7511: 0x0003) -- exactly the state
	// (UlDataAggregation=0, DlDataAggregation=0, i.e. disabled) this probe
	// found on both devices before this check was added. Force QMAP here
	// when needed, and always restore whatever was there before on exit via
	// defer: SetDataFormat is device-global, so leaving a modem in QMAP
	// mode after a diagnostic run would silently change behaviour for every
	// other client of that modem, not just this probe.
	switch {
	case *skipQMAPSetup:
		fmt.Println("-skip-qmap-setup set: leaving modem data format aggregation unchanged")
	case details.UlDataAggregation == qmi.DataAggregationProtocolQMAP && details.DlDataAggregation == qmi.DataAggregationProtocolQMAP:
		fmt.Println("modem data format aggregation is already QMAP; leaving it unchanged")
	default:
		previous := qmi.DataFormat{
			LinkProtocol:      details.LinkProtocol,
			UlDataAggregation: details.UlDataAggregation,
			DlDataAggregation: details.DlDataAggregation,
		}
		fmt.Printf("modem data format aggregation is not QMAP (ul=%d dl=%d); switching to QMAP "+
			"(link-protocol=%d ul=%d dl=%d) so BindMuxDataPort can succeed\n",
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
			return fmt.Errorf("set data format to QMAP (A1 FAILED): %w", err)
		}
		fmt.Println("modem data format switched to QMAP")

		defer func() {
			// Fresh context, not the setup ctx above: same rationale as the
			// StopNetworkInterface cleanup further below -- ctx shares the
			// 2-minute deadline with the -hold sleep and may already be
			// expired by the time this runs, which would silently no-op
			// the restore and leave the modem in QMAP mode.
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

	muxIface, err := netcfg.AddQMAPMux(*iface, uint8(*muxID))
	if err != nil {
		return fmt.Errorf("add QMAP mux (A1 FAILED): %w", err)
	}
	fmt.Printf("mux interface: %s\n", muxIface)
	defer func() {
		if err := netcfg.DelQMAPMux(*iface, uint8(*muxID)); err != nil {
			log.Printf("WARNING: leaked mux %d: %v", *muxID, err)
		}
	}()

	wds, err := qmi.NewWDSService(client)
	if err != nil {
		return fmt.Errorf("WDS service: %w", err)
	}
	defer func() {
		if err := wds.Close(); err != nil {
			log.Printf("WARNING: release WDS client: %v", err)
		}
	}()

	binding := qmi.MuxBinding{
		EpType:     uint32(*epType),
		EpIfID:     uint32(*epIface),
		MuxID:      uint8(*muxID),
		ClientType: 0x01, // QMI_WDS_CLIENT_TYPE_TETHERED
	}
	if err := wds.BindMuxDataPort(ctx, binding); err != nil {
		return fmt.Errorf("bind mux data port (A1 FAILED): %w", err)
	}
	fmt.Println("bound WDS client to mux")
	fmt.Println("A1 PASSED: QMAP mux created and data port bound")

	family := uint8(0x04)
	if *ipFamily == 6 {
		family = 0x06
	}

	handle, err := wds.StartNetworkInterface(ctx, *apn, "", "", 0, family)
	if err != nil {
		return fmt.Errorf("start network on APN %q (A2 FAILED): %w", *apn, err)
	}
	fmt.Printf("A2 PASSED: IMS PDN up, handle=%d\n", handle)
	defer func() {
		// Use a fresh context here, not the setup ctx above: ctx carries an
		// absolute 2-minute deadline shared with the -hold sleep, so on a
		// long -hold run it may already be expired by the time this runs.
		// SendRequest returns immediately on an expired context (see
		// pkg/qmi/client.go), which would silently no-op this cleanup and
		// leak the IMS PDN on the modem -- the exact hazard this probe
		// exists to catch.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := wds.StopNetworkInterface(stopCtx, handle); err != nil {
			log.Printf("WARNING: stop network: %v", err)
		}
	}()

	settings, err := wds.GetRuntimeSettings(ctx, family)
	if err != nil {
		return fmt.Errorf("get runtime settings: %w", err)
	}

	fmt.Printf("IPv4=%v IPv6=%v MTU=%d\n", settings.IPv4Address, settings.IPv6Address, settings.MTU)
	fmt.Printf("DNSv4=%v,%v DNSv6=%v,%v\n", settings.IPv4DNS1, settings.IPv4DNS2, settings.IPv6DNS1, settings.IPv6DNS2)
	fmt.Printf("IMCN flag: %v\n", settings.IMCN)
	fmt.Printf("P-CSCF via PCO: %v\n", settings.PCSCFUsingPCO)
	fmt.Printf("P-CSCF addresses: %v\n", settings.PCSCFv4)
	fmt.Printf("P-CSCF domains: %v\n", settings.PCSCFDomains)

	switch {
	case len(settings.PCSCFv4) > 0:
		fmt.Println("=> P-CSCF tier 1 (PCO) available")
	case len(settings.PCSCFDomains) > 0:
		fmt.Println("=> P-CSCF tier 2 (FQDN) available, resolution required")
	default:
		fmt.Println("=> no P-CSCF from the network; tier 3 (carrier preset) required")
	}

	fmt.Printf("holding PDN for %s — verify the default data PDN is still up\n", *hold)
	select {
	case <-time.After(*hold):
	case <-sigCtx.Done():
		fmt.Println("interrupt received, cleaning up — please wait (do not press Ctrl-C again)")
	}
	return nil
}
