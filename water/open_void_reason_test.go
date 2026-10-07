package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时的作废原因核对：状态与作废原因必须相符，沿用
// 现有作废规则——已作废样品必须保存非空的作废原因，待判定和已确认样品不能
// 带有非空的作废原因。判断原因是否有内容沿用作废操作去除首尾空白后的含义：
// voidReason 字段缺失、为 null、为空字符串或仅含空白，都视为没有原因。未确认
// 就作废与确认后作废的样品同样需要原因，是否保留逐项判定不改变这条要求。
// 任一样品违反规则，整次 Open 以 ErrCorruptRecord 失败，返回 nil 存放，错误
// 信息点到样品编号，并区分“已作废但缺少作废原因”与“未作废却保存了作废原因”
// （后一种带出记录中的状态与原因）；其他记录完整也不只读入正常部分，原文件
// 保持不变。待判定或已确认记录没有原因仍是正常数据；有效原因中的中文、标点
// 和内部空白原样保留，不为读取而改写内容。

// assertOpenRejectsVoidReason 断言作废原因核对让整次 Open 失败：返回 nil 存放、
// 错误可识别为 ErrCorruptRecord，信息包含样品编号与所有 hints（区分两种问题
// 的说明、状态或原因文本），原文件字节不被改动。
func assertOpenRejectsVoidReason(t *testing.T, dir, sampleID string, hints ...string) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("作废原因与状态不符必须让整次 Open 失败，sample=%s", sampleID)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出样品编号 %q，实际 %q", sampleID, msg)
	}
	for _, hint := range hints {
		if !strings.Contains(msg, hint) {
			t.Fatalf("错误信息应包含 %q，实际 %q", hint, msg)
		}
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// 已作废样品的 voidReason 字段整个缺失：原因丢失即损坏，不能进入按点历史
// 列表，整次打开失败并说明“已作废但缺少作废原因”。
func TestOpenVoidedMissingVoidReasonField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}], "status": "voided"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejectsVoidReason(t, dir, "S1", "已作废但缺少作废原因")
}

// 已作废样品的作废原因保存为 JSON null：与字段缺失一样是没有原因。
func TestOpenVoidedNullVoidReason(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}], "status": "voided",
      "voidReason": null}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejectsVoidReason(t, dir, "S1", "已作废但缺少作废原因")
}

// 已作废样品的作废原因为空字符串：同样是没有原因。
func TestOpenVoidedEmptyVoidReason(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusVoided, VoidReason: "",
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S1", "已作废但缺少作废原因")
}

// 已作废样品的作废原因仅含空白：沿用作废操作去除首尾空白后的含义，
// 视为没有原因，不能放行。
func TestOpenVoidedWhitespaceVoidReason(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusVoided, VoidReason: "  \t ",
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S1", "已作废但缺少作废原因")
}

// 确认后作废、仍保留逐项判定的样品丢失作废原因：是否保留逐项判定不改变
// 作废样品必须保存原因的要求，整次打开失败。
func TestOpenVoidedAfterConfirmedMissingVoidReason(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S1", "已作废但缺少作废原因")
}

// 待判定样品却保存了作废原因：整次打开失败，错误信息说明“未作废却保存了
// 作废原因”，并带出记录中的状态与原因。
func TestOpenPendingWithVoidReason(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending, VoidReason: "采样瓶破损",
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S1", "未作废却保存了作废原因", "pending", "采样瓶破损")
}

// 已确认样品却仍保存作废原因：这样的记录会重新参与最近有效结果的选择，
// 必须整次拒绝，错误信息带出记录中的状态与原因。
func TestOpenConfirmedWithVoidReason(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, VoidReason: "采样瓶破损", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S1", "未作废却保存了作废原因", "confirmed", "采样瓶破损")
}

// 同一文件中其他记录完整，也不能只读入正常部分：一份作废原因与状态不符
// 即整次失败，正常样品同样读不到。
func TestOpenVoidReasonMismatchRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 完整、正常的已确认样品。
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
			// 完整、正常的已作废样品。
			"S-VOID": {
				ID: "S-VOID", PointID: "P1", SampledAt: at(6, 0),
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
			// 已作废但原因丢失的损坏样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided,
			},
		},
	})
	assertOpenRejectsVoidReason(t, dir, "S-BAD", "已作废但缺少作废原因")
}

// 有效原因中的中文、标点和内部空白原样保留，不为读取而改写内容；确认后作废
// 的样品保留当时的测量值、上限、生效时间和结论，只供历史核对：按点列表能查
// 到它，最近有效结果跳过它，再次确认仍拒绝。
func TestOpenVoidReasonPreservedVerbatim(t *testing.T) {
	dir := t.TempDir()
	reason := "采样瓶破损，复测 确认：样品污染"
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: reason, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("作废原因完整的记录应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点列表应能查到作废样品: %+v err=%v", list, err)
	}
	smp := list[0]
	if smp.Status != StatusVoided || smp.VoidReason != reason {
		t.Fatalf("作废原因必须原样保留，不为读取改写: %+v", smp)
	}
	if r := smp.Results[0]; r.Value != 9 || r.Limit != 8 ||
		!r.LimitEffective.Equal(openEff()) || !r.Exceeded || !smp.Exceeded {
		t.Fatalf("确认后作废的测量值、上限、生效时间与结论必须保留: %+v", smp)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废样品不应作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("已作废样品再确认应被拒绝，got %v", err)
	}
}

// 待判定或已确认记录没有原因仍是正常数据，不要求补写字段；仅含空白的原因
// 也按没有原因处理，不为读取而改写内容。
func TestOpenNonVoidedWithoutReasonIsNormal(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 待判定，没有 voidReason 字段。
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			// 已确认，没有 voidReason 字段。
			"S2": {
				ID: "S2", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
			// 待判定，原因仅含空白：按没有原因处理。
			"S3": {
				ID: "S3", PointID: "P1", SampledAt: at(12, 0),
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusPending, VoidReason: "   ",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("未作废且没有原因的记录是正常数据，应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 3 {
		t.Fatalf("三份样品都应读入: %+v err=%v", list, err)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S2" {
		t.Fatalf("最近有效结果应为 S2: %+v ok=%v err=%v", latest, ok, err)
	}
}
