package water

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对样品状态与作废原因是否相符的核对：
// 已作废样品必须保存去掉首尾空白后非空的作废原因（未确认就作废与确认后
// 作废同样需要，是否保留逐项判定不改变这条要求）；待判定和已确认样品
// 不能带有非空的作废原因。voidReason 字段缺失、为 null、为空字符串或
// 仅含空白都视为没有原因。违反任一条都让整次 Open 以 ErrCorruptRecord
// 失败、返回 nil 存放，错误信息点名样品编号并区分两种问题，未作废却带
// 原因的还要带出记录中的状态与原因；其他样品再正常也不部分读入，原文件
// 字节不变。有效原因中的中文、标点与内部空白原样保留，读入不做改写；
// 正常录入、确认、作废入口及查询资格不受这项读取检查影响。

// assertOpenRejectsReason 断言打开整次失败：返回 nil 存放、错误可识别为
// ErrCorruptRecord，信息包含样品编号与给出的全部片段，且原文件字节不变。
func assertOpenRejectsReason(t *testing.T, dir, sampleID string, fragments ...string) {
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
		t.Fatalf("状态与作废原因不符必须让整次 Open 失败，sample=%s", sampleID)
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
	for _, f := range fragments {
		if !strings.Contains(msg, f) {
			t.Fatalf("错误信息应包含 %q，实际 %q", f, msg)
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

// writeVoidedRawFile 直接写出一份作废样品的原始 JSON，便于构造正常作废
// 入口不会产生的原因字段形态（缺失、null、空字符串、仅含空白）。
// afterConfirmed 为 true 时附带一份完整自洽的历史判定依据，用来证明
// 即使逐项依据齐全也不能代替作废原因。
func writeVoidedRawFile(t *testing.T, dir string, afterConfirmed bool, reasonField string) {
	t.Helper()
	resultTail := ""
	if afterConfirmed {
		resultTail = `,"exceeded":true,"results":[{"item":"` + openPH +
			`","value":9,"limit":8,"limitEffective":"` +
			openEff().Format(time.RFC3339Nano) + `","exceeded":true}]`
	}
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) +
		`", "measurements": [{"item": "` + openPH + `", "value": 9}], "status": "voided"` +
		resultTail + reasonField + `}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

// 已作废样品缺少作废原因：无论未确认就作废还是确认后作废、是否保留逐项
// 判定，原因字段缺失、为 null、为空字符串或仅含空白都必须整次拒绝，
// 错误明确是“已作废但缺少作废原因”。
func TestOpenVoidedMissingReasonRejects(t *testing.T) {
	cases := []struct {
		name           string
		afterConfirmed bool
		reasonField    string
	}{
		{"未确认作废_原因字段缺失", false, ``},
		{"未确认作废_原因为null", false, `, "voidReason": null`},
		{"未确认作废_原因为空字符串", false, `, "voidReason": ""`},
		{"未确认作废_原因仅含空格", false, `, "voidReason": "   "`},
		{"未确认作废_原因仅含制表符换行", false, `, "voidReason": "\t\n "`},
		{"确认后作废_原因字段缺失", true, ``},
		{"确认后作废_原因为null", true, `, "voidReason": null`},
		{"确认后作废_原因为空字符串", true, `, "voidReason": ""`},
		{"确认后作废_原因仅含空白", true, `, "voidReason": "  \t "`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeVoidedRawFile(t, dir, c.afterConfirmed, c.reasonField)
			assertOpenRejectsReason(t, dir, "S1", "已作废但缺少作废原因")
		})
	}
}

// 待判定样品保存了非空作废原因：即使原测量完整也必须整次拒绝，错误说明
// 它“未作废却保存了作废原因”，并带出 pending/待判定状态与原因原文。
func TestOpenPendingWithVoidReasonRejects(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending, VoidReason: "采样瓶破损",
			},
		},
	})
	assertOpenRejectsReason(t, dir, "S1",
		"未作废却保存了作废原因", "pending", "待判定", "采样瓶破损")
}

// 已确认样品保存了非空作废原因：否则它会一边参与最近有效结果的选择、
// 一边留着作废原因。错误带出 confirmed/已确认状态与含中文、标点、内部
// 空白的原因原文；其余判定依据完整也不能放行。
func TestOpenConfirmedWithVoidReasonRejects(t *testing.T) {
	reason := "采样瓶 破损（复测），作废！"
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, VoidReason: reason, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejectsReason(t, dir, "S1",
		"未作废却保存了作废原因", "confirmed", "已确认", reason)
}

// 带出原因时按记录中的原文呈现：首尾空白也在引号原文里，不能只带出
// TrimSpace 后的文本，更不能替它把状态改成已作废。
func TestOpenNonVoidedReasonQuotedVerbatim(t *testing.T) {
	reason := "  采样瓶破损  "
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "status": "pending", "voidReason": "  采样瓶破损  "}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejectsReason(t, dir, "S1",
		"未作废却保存了作废原因", "pending", "待判定", fmt.Sprintf("%q", reason))
}

// 待判定或已确认记录的原因仅含空白时，按规则视为没有原因，仍是正常数据：
// 不要求补写或删除字段，读入后状态与有效资格不变。
func TestOpenNonVoidedWhitespaceOnlyReasonIsNormal(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 待判定样品带着仅含空白的原因：视为没有原因，正常读入。
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending, VoidReason: "   ",
			},
			// 已确认样品带着仅含制表符的原因：同样视为没有原因，
			// 完整自洽的判定依据照常有效，仍是该点的最近有效结果。
			"SC": {
				ID: "SC", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, VoidReason: "\t ", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("仅含空白的原因视为没有原因，记录应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || !ok || latest.ID != "SC" {
		t.Fatalf("已确认记录的有效资格不应受空白原因影响: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 有效作废原因中的中文、标点和内部空白原样保留，不为读取而改写；原因带
// 首尾空白时（正常作废入口会先去掉首尾空白，这种记录只能来自文件外部
// 改动）按去除首尾空白后非空判断仍属有效原因，同样原样读入、不补不改。
// 作废记录按点列表可查、最近有效结果跳过、再次确认仍拒绝。
func TestOpenValidVoidReasonPreserved(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "status": "voided", "voidReason": "采样瓶 破损（第一批），需复测！"},
    "S2": {"id": "S2", "pointId": "P1", "sampledAt": "` +
		at(11, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "voided", "voidReason": " 采样瓶破损 ",
      "exceeded": false,
      "results": [{"item": "` + openPH + `", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("非空作废原因（含内部空白或首尾空白）都应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("作废记录按点列表应都可查: %+v err=%v", list, err)
	}
	byID := map[string]Sample{}
	for _, smp := range list {
		byID[smp.ID] = smp
	}
	if got := byID["S1"].VoidReason; got != "采样瓶 破损（第一批），需复测！" {
		t.Fatalf("原因中的中文、标点与内部空白必须原样保留: %q", got)
	}
	if got := byID["S2"].VoidReason; got != " 采样瓶破损 " {
		t.Fatalf("读取不得改写原因原文（含首尾空白）: %q", got)
	}
	// 两份都是作废记录：最近有效结果明确为空。
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废样品不能成为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	// 再次确认仍拒绝，历史依据保留情况不影响作废资格。
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("未确认就作废的样品再确认应拒绝，got %v", err)
	}
	if _, err := s.Confirm("S2"); !errors.Is(err, ErrVoided) {
		t.Fatalf("确认后作废的样品再确认应拒绝，got %v", err)
	}
}

// 一份样品违反状态与原因的核对，即使同文件其他样品完整也整次失败、
// 返回 nil 存放，不能只读入正常部分；原文件保持不变。
func TestOpenVoidReasonMismatchRejectsWholeFile(t *testing.T) {
	cases := []struct {
		name      string
		bad       *Sample
		fragments []string
	}{
		{
			name: "已作废但原因丢失",
			bad: &Sample{
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided,
			},
			fragments: []string{"已作废但缺少作废原因"},
		},
		{
			name: "已确认却带着作废原因",
			bad: &Sample{
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, VoidReason: "采样瓶破损", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
			fragments: []string{"未作废却保存了作废原因", "confirmed", "已确认", "采样瓶破损"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
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
					"S-BAD": c.bad,
				},
			})
			assertOpenRejectsReason(t, dir, "S-BAD", c.fragments...)
		})
	}
}

// 原因核对在状态核对之后：状态本身缺失或不受支持时，即使带着非空原因，
// 也仍按状态问题拒绝，不能把它报成“未作废却保存了作废原因”，更不能凭
// 原因猜出作废状态。
func TestOpenVoidReasonCheckedAfterStatus(t *testing.T) {
	cases := []struct {
		name       string
		statusJSON string
	}{
		{"状态为null", `null`},
		{"状态为空字符串", `""`},
		{"状态字段缺失", ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			statusField := ""
			if c.statusJSON != `` {
				statusField = `, "status": ` + c.statusJSON
			}
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
				at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "voidReason": "采样瓶破损"` + statusField + `}
  }
}`
			if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
				t.Fatalf("write data file: %v", err)
			}
			got, err := Open(dir)
			if err == nil {
				if got != nil {
					got.Close()
				}
				t.Fatalf("状态缺失必须整次 Open 失败")
			}
			if got != nil {
				t.Fatalf("打开失败时不得返回数据存放对象，got %#v", got)
			}
			if !errors.Is(err, ErrCorruptRecord) {
				t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "缺少状态") {
				t.Fatalf("应先报状态缺失，实际 %q", msg)
			}
			if strings.Contains(msg, "已作废但缺少作废原因") ||
				strings.Contains(msg, "未作废却保存了作废原因") {
				t.Fatalf("状态缺失时不应抢先报状态与作废原因不符，实际 %q", msg)
			}
		})
	}

	// 不支持的状态值同样先报状态问题，未知字符串原样带出。
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "status": "archived", "voidReason": "采样瓶破损"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("不支持的状态值必须整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "archived") || !strings.Contains(msg, "不受支持") {
		t.Fatalf("应报不支持的状态值并原样带出，实际 %q", msg)
	}
	if strings.Contains(msg, "未作废却保存了作废原因") {
		t.Fatalf("状态不受支持时不应抢先报作废原因问题，实际 %q", msg)
	}
}

// 正常录入、确认、作废落盘的数据重开后行为不变：两种来路的作废原因都
// 原样可查（含中文、标点与内部空白），按点列表保留作废记录，最近有效
// 结果跳过作废样品，再次确认仍返回 ErrVoided。
func TestReopenNormalVoidFlowKeepsReasonAndQualification(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	// P1：录入后不确认直接作废，原因含内部空白与标点。
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	v1, err := s.Void("S1", "录入 信息有误（第一批）！")
	if err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	if v1.VoidReason != "录入 信息有误（第一批）！" {
		t.Fatalf("作废返回的原因应保留内部空白与标点: %q", v1.VoidReason)
	}

	// P2：确认超标后再作废，历史依据与原因一起保留。
	mustSample(t, s, "S2", "P2", at(11, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S2"); err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	if _, err := s.Void("S2", "采样瓶 破损，需复测"); err != nil {
		t.Fatalf("Void S2: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("正常作废落盘的数据应照常重新打开: %v", err)
	}
	defer reopened.Close()

	p1, err := reopened.ListByPoint("P1")
	if err != nil || len(p1) != 1 || p1[0].VoidReason != "录入 信息有误（第一批）！" {
		t.Fatalf("重开后未确认作废记录的原因应原样保留: %+v err=%v", p1, err)
	}
	if len(p1[0].Results) != 0 {
		t.Fatalf("未确认就作废的样品重开后仍应没有判定依据: %+v", p1[0])
	}
	p2, err := reopened.ListByPoint("P2")
	if err != nil || len(p2) != 1 {
		t.Fatalf("P2 按点列表: %+v err=%v", p2, err)
	}
	if p2[0].VoidReason != "采样瓶 破损，需复测" || !p2[0].Exceeded || len(p2[0].Results) != 1 {
		t.Fatalf("重开后确认作废记录的原因与历史依据应原样保留: %+v", p2[0])
	}
	for _, point := range []string{"P1", "P2"} {
		if latest, ok, err := reopened.LatestResult(point); err != nil || ok {
			t.Fatalf("%s 重开后应无最近有效结果: %+v ok=%v err=%v", point, latest, ok, err)
		}
	}
	for _, id := range []string{"S1", "S2"} {
		if _, err := reopened.Confirm(id); !errors.Is(err, ErrVoided) {
			t.Fatalf("重开后作废样品 %s 再确认应拒绝，got %v", id, err)
		}
	}
}
