package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func main() {
	devicePath := flag.String("device", defaultQmiDevice(), "Path to QMI device")
	action := flag.String("action", "all", "Action: all, serving, signal, signal-info, sysinfo, scan, cell-info, register, dump")
	useQRTR := flag.Bool("qrtr", false, "Use native QRTR (AF_QIPCRTR) transport instead of a cdc-wdm device")
	flag.Parse()

	client, err := qmi.NewClientWithOptions(context.Background(), *devicePath, qmi.ClientOptions{UseQRTR: *useQRTR})
	if err != nil {
		log.Fatalf("Failed to create QMI client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nas, err := qmi.NewNASService(client)
	if err != nil {
		log.Fatalf("Failed to create NAS service: %v", err)
	}
	defer nas.Close()

	switch *action {
	case "all":
		runRegister(nas)
		runServing(ctx, nas)
		runSignal(ctx, nas)
		runSignalInfo(ctx, nas)
		runSysInfo(ctx, nas)
		runScan(nas)
		runCellLocationInfo(ctx, nas)
	case "serving":
		runServing(ctx, nas)
	case "signal":
		runSignal(ctx, nas)
	case "signal-info":
		runSignalInfo(ctx, nas)
	case "sysinfo":
		runSysInfo(ctx, nas)
	case "scan":
		runScan(nas)
	case "cell-info":
		runCellLocationInfo(ctx, nas)
	case "register":
		runRegister(nas)
	case "dump":
		runDump(ctx, client, nas)
	default:
		log.Fatalf("Unknown action: %s", *action)
	}
}

func runRegister(nas *qmi.NASService) {
	fmt.Println("=== NAS Register Indications ===")
	if err := nas.RegisterIndications(); err != nil {
		log.Printf("RegisterIndications failed: %v", err)
		return
	}
	fmt.Println("OK")
	fmt.Println()
}

func runServing(ctx context.Context, nas *qmi.NASService) {
	fmt.Println("=== NAS Serving System ===")
	ss, err := nas.GetServingSystem(ctx)
	if err != nil {
		log.Printf("GetServingSystem failed: %v", err)
		return
	}
	fmt.Printf("Registration: %s (%d)\n", ss.RegistrationState.String(), ss.RegistrationState)
	fmt.Printf("PSAttached: %v\n", ss.PSAttached)
	fmt.Printf("RadioInterface: %d\n", ss.RadioInterface)
	fmt.Printf("MCC: %d\n", ss.MCC)
	fmt.Printf("MNC: %d\n", ss.MNC)
	fmt.Println()
}

func runSignal(ctx context.Context, nas *qmi.NASService) {
	fmt.Println("=== NAS Signal Strength ===")
	s, err := nas.GetSignalStrength(ctx)
	if err != nil {
		log.Printf("GetSignalStrength failed: %v", err)
		return
	}
	fmt.Printf("RSSI: %d\n", s.RSSI)
	fmt.Printf("RSRQ: %d\n", s.RSRQ)
	fmt.Printf("RSRP: %d\n", s.RSRP)
	fmt.Printf("ECIO: %d\n", s.ECIO)
	fmt.Printf("SNR: %d\n", s.SNR)
	fmt.Println()
}

func runSignalInfo(ctx context.Context, nas *qmi.NASService) {
	fmt.Println("=== NAS Signal Info ===")
	info, err := nas.GetSignalInfo(ctx)
	if err != nil {
		log.Printf("GetSignalInfo failed: %v", err)
		return
	}
	if info.LTE != nil {
		fmt.Printf("LTE RSRP: %s\n", formatOptionalDB(info.LTE.RSRP, 1))
		fmt.Printf("LTE RSRQ: %s\n", formatOptionalDB(info.LTE.RSRQ, 1))
		fmt.Printf("LTE SNR: %s\n", formatOptionalDB(info.LTE.SNR, 10))
	}
	if info.NR5G != nil {
		fmt.Printf("NR5G RSRP: %s\n", formatOptionalDB(info.NR5G.RSRP, 1))
		fmt.Printf("NR5G RSRQ: %s\n", formatOptionalDB(info.NR5G.RSRQ, 1))
		fmt.Printf("NR5G SNR: %s\n", formatOptionalDB(info.NR5G.SNR, 10))
	}
	fmt.Println()
}

func formatOptionalDB(value *int16, scale float64) string {
	if value == nil {
		return "n/a"
	}
	if scale == 1 {
		return strconv.FormatInt(int64(*value), 10)
	}
	return fmt.Sprintf("%.1f", float64(*value)/scale)
}

func runSysInfo(ctx context.Context, nas *qmi.NASService) {
	fmt.Println("=== NAS Sys Info ===")
	info, err := nas.GetSysInfo(ctx)
	if err != nil {
		log.Printf("GetSysInfo failed: %v", err)
		return
	}
	fmt.Printf("CellID: %d\n", info.CellID)
	fmt.Printf("TAC: %d\n", info.TAC)
	fmt.Printf("LAC: %d\n", info.LAC)
	fmt.Println()
}

func runScan(nas *qmi.NASService) {
	fmt.Println("=== NAS Network Scan ===")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := nas.PerformNetworkScan(ctx)
	if err != nil {
		log.Printf("PerformNetworkScan failed: %v", err)
		return
	}
	for _, r := range res {
		fmt.Printf("PLMN: %s-%s status=%d rats=%v desc=%q\n", r.MCC, r.MNC, r.Status, r.RATs, r.Description)
	}
	fmt.Println()
}

func runCellLocationInfo(ctx context.Context, nas *qmi.NASService) {
	info, err := nas.GetCellLocationInfo(ctx)
	if err != nil {
		log.Printf("GetCellLocationInfo failed: %v", err)
		return
	}
	fmt.Print(formatCellLocationInfo(info))
}

func formatCellLocationInfo(info *qmi.CellLocationInfo) string {
	var output bytes.Buffer
	output.WriteString("=== NAS Cell Location Info ===\n")
	if info == nil {
		return output.String()
	}
	if info.LTE != nil {
		fmt.Fprintf(&output, "LTE: PLMN=%s-%s EARFCN=%d PCI=%d\n", info.LTE.MCC, info.LTE.MNC, info.LTE.EARFCN, info.LTE.ServingCellID)
		for _, neighbor := range info.LTE.IntraFrequencyNeighbors {
			fmt.Fprintf(&output, "LTE neighbor PCI=%d RSRP=%.1f dBm RSRQ=%.1f dB\n", neighbor.PhysicalCellID, float64(neighbor.RSRP)/10, float64(neighbor.RSRQ)/10)
		}
	}
	if info.NR5G != nil {
		fmt.Fprintf(&output, "NR5G: PLMN=%s-%s PCI=%d RSRP: %s dBm RSRQ: %s dB SNR: %s dB\n", info.NR5G.MCC, info.NR5G.MNC, info.NR5G.PhysicalCellID, formatOptionalDB(info.NR5G.RSRP, 10), formatOptionalDB(info.NR5G.RSRQ, 10), formatOptionalDB(info.NR5G.SNR, 10))
	}
	output.WriteByte('\n')
	return output.String()
}

func runDump(ctx context.Context, client *qmi.Client, nas *qmi.NASService) {
	fmt.Println("=== NAS Dump TLVs ===")

	type item struct {
		name   string
		msgID  uint16
		client uint8
	}

	items := []item{
		{name: "NASGetServingSystem", msgID: qmi.NASGetServingSystem, client: nas.ClientID()},
		{name: "NASGetSignalStrength", msgID: qmi.NASGetSignalStrength, client: nas.ClientID()},
		{name: "NASGetSysInfo", msgID: qmi.NASGetSysInfo, client: nas.ClientID()},
		{name: "NASGetSignalInfo", msgID: 0x004F, client: nas.ClientID()},
		{name: "NASPerformNetworkScan", msgID: qmi.NASPerformNetworkScan, client: nas.ClientID()},
	}

	for _, it := range items {
		resp, err := client.SendRequest(ctx, qmi.ServiceNAS, it.client, it.msgID, nil)
		if err != nil {
			fmt.Printf("%s: request error: %v\n", it.name, err)
			continue
		}
		if err := resp.CheckResult(); err != nil {
			fmt.Printf("%s: result error: %v\n", it.name, err)
			continue
		}
		fmt.Printf("%s: %d TLVs\n", it.name, len(resp.TLVs))
		for _, tlv := range resp.TLVs {
			fmt.Printf("  TLV 0x%02x len=%d val=%x\n", tlv.Type, len(tlv.Value), tlv.Value)
		}
	}
	fmt.Println()
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
