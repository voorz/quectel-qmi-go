package manager

import (
	"context"
	"strings"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

type UIMReadinessReason string

const (
	UIMReadinessReady              UIMReadinessReason = "ready"
	UIMReadinessTransportFatal     UIMReadinessReason = "transport_fatal"
	UIMReadinessControlUnavailable UIMReadinessReason = "control_unavailable"
	UIMReadinessCardAbsent         UIMReadinessReason = "card_absent"
	UIMReadinessCardResetting      UIMReadinessReason = "card_resetting"
	UIMReadinessSIMBlocked         UIMReadinessReason = "sim_blocked"
	UIMReadinessIdentityEmpty      UIMReadinessReason = "identity_empty"
	UIMReadinessNeedsProvisioning  UIMReadinessReason = "needs_provisioning"
)

type UIMReadiness struct {
	TransportReady     bool
	ControlReady       bool
	UIMReady           bool
	CardPresent        bool
	SIMStatus          qmi.SIMStatus
	ActiveSlot         uint8
	SlotKnown          bool
	SlotSource         string
	ActivePhysicalSlot uint8 // 激活槽的物理槽位置（1-based，对应 qmicli 的 "Physical slot N"），与 ActiveSlot 的逻辑槽号语义不同
	PhysicalSlotKnown  bool
	ICCID              string
	IMSI               string
	AppState           uint8
	ProvisioningActive bool
	NeedsProvisioning  bool
	Reason             UIMReadinessReason
	ActiveSlotIsEUICC  bool  // 激活槽内是否为 eUICC（eSIM 芯片）
	PIN1Retries        uint8 // PIN1 剩余验证次数
	PUK1Retries        uint8 // PUK1 剩余解锁次数

	// SIMStatus 会把「卡错误」与「PIN 永久锁死」都归成 qmi.SIMBlocked，
	// 上层只看它无法区分。以下两个字段保留原始判据：
	//   CardState: 0=absent 1=present 2=error（QMI_UIM_CARD_STATE_*）
	//   PIN1State: 仅当 PINStatusBlocked/PermBlocked 时才是真的被 PIN 锁住
	CardState uint8
	PIN1State qmi.PINStatus

	CardDetailsKnown bool // CardState/PIN1State/PIN1Retries/PUK1Retries 是否取到有效值
	Err              error
}

func isUIMReadinessTransportFatal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "qmi-proxy") && strings.Contains(msg, "broken pipe") {
		return true
	}
	for _, fragment := range []string{
		"qmi: read failed",
		"qmi read failed",
		"failed to open qmi device",
		"no such device",
		"no such file or directory",
		"client closed",
		"read failed: eof",
		"read failed eof",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

func resolveActiveUIMSlot(info *qmi.UIMSlotStatus) (uint8, bool, string) {
	if info == nil {
		return 0, false, ""
	}
	for idx, slot := range info.Slots {
		if slot.PhysicalCardStatus != qmi.UIMPhysicalCardStatePresent {
			continue
		}
		if slot.PhysicalSlotStatus != qmi.UIMSlotStateActive {
			continue
		}
		if slot.LogicalSlot != 0 {
			return slot.LogicalSlot, true, "uim_slot_status"
		}
		return uint8(idx + 1), true, "uim_slot_status_index"
	}
	return 0, false, ""
}

// resolveActiveUIMPhysicalSlot 返回激活槽的物理槽位置（1-based，即数组下标+1，
// 对应 qmicli --uim-get-slot-status 打印的 "Physical slot N"）。
// 与 resolveActiveUIMSlot 不同：本函数忽略 LogicalSlot，因为逻辑槽号与物理槽位置
// 在多物理槽 eSIM 设备上可能不一致（例如物理槽 2 插卡，但其 LogicalSlot 为 1）。
// resolveActiveUIMSlot 服务于切卡收敛等需要逻辑槽号寻址的场景，语义不应改变；
// 本函数专供人类可读的卡槽展示使用。
func resolveActiveUIMPhysicalSlot(info *qmi.UIMSlotStatus) (uint8, bool) {
	if info == nil {
		return 0, false
	}
	for idx, slot := range info.Slots {
		if slot.PhysicalCardStatus != qmi.UIMPhysicalCardStatePresent {
			continue
		}
		if slot.PhysicalSlotStatus != qmi.UIMSlotStateActive {
			continue
		}
		return uint8(idx + 1), true
	}
	return 0, false
}

// resolveActiveUIMSlotEUICC 判定当前激活槽内的卡是否为 eUICC。
// 判定条件与 resolveActiveUIMSlot 完全一致：卡在位且槽处于激活态。
func resolveActiveUIMSlotEUICC(info *qmi.UIMSlotStatus) bool {
	if info == nil {
		return false
	}
	for _, slot := range info.Slots {
		if slot.PhysicalCardStatus != qmi.UIMPhysicalCardStatePresent {
			continue
		}
		if slot.PhysicalSlotStatus != qmi.UIMSlotStateActive {
			continue
		}
		return slot.IsEUICC
	}
	return false
}

func buildUIMReadiness(status qmi.SIMStatus, details *qmi.CardStatusDetails, slotInfo *qmi.UIMSlotStatus, ids DeviceIdentities, sourceErr error) UIMReadiness {
	return buildUIMReadinessWithSlotError(status, details, slotInfo, ids, sourceErr, nil)
}

func buildUIMReadinessWithSlotError(status qmi.SIMStatus, details *qmi.CardStatusDetails, slotInfo *qmi.UIMSlotStatus, ids DeviceIdentities, cardErr error, slotErr error) UIMReadiness {
	slot, slotKnown, slotSource := resolveActiveUIMSlot(slotInfo)
	sourceErr := cardErr
	if sourceErr == nil {
		sourceErr = slotErr
	}
	out := UIMReadiness{
		TransportReady: true,
		ControlReady:   true,
		SIMStatus:      status,
		ActiveSlot:     slot,
		SlotKnown:      slotKnown,
		SlotSource:     slotSource,
		ICCID:          strings.TrimSpace(ids.ICCID),
		IMSI:           strings.TrimSpace(ids.IMSI),
		Err:            sourceErr,
	}
	out.ActivePhysicalSlot, out.PhysicalSlotKnown = resolveActiveUIMPhysicalSlot(slotInfo)
	out.ActiveSlotIsEUICC = resolveActiveUIMSlotEUICC(slotInfo)
	if details != nil {
		out.CardDetailsKnown = true
		out.PIN1Retries = details.PIN1Retries
		out.PUK1Retries = details.PUK1Retries
		out.CardState = details.CardState
		out.PIN1State = details.PIN1State
	}

	if cardErr != nil {
		if isUIMReadinessTransportFatal(cardErr) {
			out.TransportReady = false
			out.ControlReady = false
			out.Reason = UIMReadinessTransportFatal
			return out
		}
		out.ControlReady = false
		out.Reason = UIMReadinessControlUnavailable
		return out
	}
	if isUIMReadinessTransportFatal(slotErr) {
		out.TransportReady = false
		out.ControlReady = false
		out.Reason = UIMReadinessTransportFatal
		return out
	}

	out.CardPresent = status != qmi.SIMAbsent
	if details != nil {
		out.AppState = details.AppState
		out.ProvisioningActive = details.AppState == qmi.UIMAppStateReady

		switch details.CardState {
		case 0x00:
			out.CardPresent = false
		case 0x01, 0x02:
			out.CardPresent = true
		}
	}
	if !out.CardPresent {
		out.Reason = UIMReadinessCardAbsent
		return out
	}
	if status == qmi.SIMBlocked || status == qmi.SIMPINRequired || status == qmi.SIMPUKRequired || status == qmi.SIMNetworkLocked {
		out.Reason = UIMReadinessSIMBlocked
		return out
	}

	if out.CardPresent && details != nil && details.AppState == qmi.UIMAppStateDetected {
		out.NeedsProvisioning = true
		out.Reason = UIMReadinessNeedsProvisioning
		return out
	}

	if status != qmi.SIMReady {
		out.Reason = UIMReadinessCardResetting
		return out
	}

	out.UIMReady = true
	if out.ICCID == "" && out.IMSI == "" {
		out.Reason = UIMReadinessIdentityEmpty
		return out
	}
	out.Reason = UIMReadinessReady
	return out
}

func (m *Manager) GetUIMReadiness(ctx context.Context) (UIMReadiness, error) {
	return withCardAccessValue(m, ctx, func() (UIMReadiness, error) {
		return m.getUIMReadiness(ctx)
	})
}

func (m *Manager) getUIMReadiness(ctx context.Context) (UIMReadiness, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		err := ErrServiceNotReady("UIM")
		return buildUIMReadiness(qmi.SIMNotReady, nil, nil, DeviceIdentities{}, err), err
	}

	type cardStatusResult struct {
		details *qmi.CardStatusDetails
		status  qmi.SIMStatus
	}
	var details *qmi.CardStatusDetails
	status := qmi.SIMNotReady
	cardStatus, cardErr := withUIMRecoveryValue(m, "GetUIMReadiness.GetCardStatusDetails", func(uim *qmi.UIMService) (cardStatusResult, error) {
		details, status, err := uim.GetCardStatusDetails(ctx)
		return cardStatusResult{details: details, status: status}, err
	})
	if cardErr == nil {
		details = cardStatus.details
		status = cardStatus.status
	}

	var slotInfo *qmi.UIMSlotStatus
	var slotErr error
	if cardErr == nil {
		slotInfo, slotErr = withUIMRecoveryValue(m, "GetUIMReadiness.GetSlotStatus", func(uim *qmi.UIMService) (*qmi.UIMSlotStatus, error) {
			return uim.GetSlotStatus(ctx)
		})
	}

	ids, _ := m.GetCachedIdentities()
	if cardErr == nil && strings.TrimSpace(ids.ICCID) == "" {
		if iccid, err := m.getICCIDStrictLive(ctx); err == nil {
			ids.ICCID = iccid
		}
	}
	if cardErr == nil && strings.TrimSpace(ids.IMSI) == "" {
		if imsi, err := m.getIMSIStrictLive(ctx); err == nil {
			ids.IMSI = imsi
		}
	}

	readiness := buildUIMReadinessWithSlotError(status, details, slotInfo, ids, cardErr, slotErr)
	if cardErr != nil {
		return readiness, cardErr
	}
	if isUIMReadinessTransportFatal(slotErr) {
		return readiness, slotErr
	}
	return readiness, nil
}
