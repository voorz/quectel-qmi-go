package qmi

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func requestUIMFileID(req *Packet) uint16 {
	tlv := FindTLV(req.TLVs, 0x02)
	if tlv == nil || len(tlv.Value) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(tlv.Value[:2])
}

func TestGetHomePLMNUsesIMSIAndEFADDespiteConflictingSelectors(t *testing.T) {
	client := newUIMUnitTestClient()
	var files []uint16
	stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
		if req.MessageID != UIMReadTransparent {
			t.Errorf("unexpected message id 0x%04x", req.MessageID)
			return qmiErrorPacket(QMIErrInvalidArg)
		}
		fileID := requestUIMFileID(req)
		files = append(files, fileID)
		switch fileID {
		case 0x6FAD:
			return readTransparentPacket([]byte{0x00, 0x00, 0x00, 0x02})
		case 0x6F07:
			return readTransparentPacket([]byte{0x08, 0x29, 0x43, 0x33, 0x56, 0x57, 0x68, 0x48, 0x43})
		case 0x6F62, 0x6FD9:
			t.Errorf("selector file 0x%04X must not be read", fileID)
		}
		return qmiErrorPacket(QMIErrInvalidArg)
	})
	defer stop()

	uim := &UIMService{client: client, clientID: 1}
	mcc, mnc, err := uim.GetHomePLMN(context.Background())
	if err != nil {
		t.Fatalf("GetHomePLMN() error = %v", err)
	}
	if mcc != "234" || mnc != "33" {
		t.Fatalf("GetHomePLMN() = %q/%q, want 234/33", mcc, mnc)
	}
	if len(files) != 2 || files[0] != 0x6FAD || files[1] != 0x6F07 {
		t.Fatalf("transparent files read = %X, want EF_AD then EF_IMSI", files)
	}
}

func TestGetHomePLMNRejectsInvalidEFADWithoutSelectorFallback(t *testing.T) {
	client := newUIMUnitTestClient()
	var files []uint16
	stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
		if req.MessageID != UIMReadTransparent {
			t.Errorf("unexpected message id 0x%04x", req.MessageID)
			return qmiErrorPacket(QMIErrInvalidArg)
		}
		fileID := requestUIMFileID(req)
		files = append(files, fileID)
		if fileID == 0x6F62 || fileID == 0x6FD9 || fileID == 0x6F07 {
			t.Errorf("removed selector/IMSI fallback file 0x%04X was read", fileID)
		}
		if fileID == 0x6FAD {
			return readTransparentPacket([]byte{0x00, 0x00, 0x00, 0x00})
		}
		return qmiErrorPacket(QMIErrInvalidArg)
	})
	defer stop()

	uim := &UIMService{client: client, clientID: 1}
	if _, _, err := uim.GetHomePLMN(context.Background()); err == nil || !strings.Contains(err.Error(), "mnc_length_unknown") {
		t.Fatalf("GetHomePLMN() error = %v, want mnc_length_unknown", err)
	}
	for _, fileID := range files {
		if fileID == 0x6F62 || fileID == 0x6FD9 || fileID == 0x6F07 {
			t.Fatalf("GetHomePLMN() read removed selector/IMSI fallback: %X", files)
		}
	}
}
