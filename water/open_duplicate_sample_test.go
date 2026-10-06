package water

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对样品编号唯一性的核对：
// 样品编号在本地 samples 样品集合中只能出现一次。JSON 反序列化进 map 时同键的
// 后一条记录会悄悄覆盖前一条，已确认的超标结论可能被另一条同编号达标结论替换，
// 而逐份完整性校验只能看到幸存条目；因此 Open 必须在逐份校验之前按文件中的原始
// JSON 标记核对样品集合的键。即使两条记录内容完全相同也算重复，不按排列顺序择一；
// 待判定、已确认、已作废样品一律适用；同一编号一处直接写出、另一处写成 Unicode
// 转义仍是重复，中间隔着其他样品也一样。发现重复时整次 Open 失败：
// errors.Is(err, ErrCorruptRecord)、返回 nil 存放、错误信息指出重复编号并说明
// 样品集合中存在重复编号（区别于同一份样品内部测量项目重复），原文件字节不变。
// 不同样品各含同一测量项目是正常数据，空样品集合照常打开。

// assertOpenRejectsDuplicateSampleID 断言打开因样品集合内编号重复而整次失败：
// nil 存放、ErrCorruptRecord、信息点名重复编号并说明样品集合中存在重复编号，
// 且与“同一份样品内部测量项目重复”的错误信息可区分；原文件字节不变。
func assertOpenRejectsDuplicateSampleID(t *testing.T, dir, sampleID string) {
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
		t.Fatalf("样品集合内编号 %s 重复必须让整次 Open 失败", sampleID)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出重复的样品编号 %q，实际 %q", sampleID, msg)
	}
	if !strings.Contains(msg, "样品集合") || !strings.Contains(msg, "重复") {
		t.Fatalf("错误信息应说明样品集合中存在重复编号，实际 %q", msg)
	}
	if !strings.Contains(msg, "样品集合中存在重复的样品编号") {
		t.Fatalf("错误信息应明确为样品集合内编号重复，实际 %q", msg)
	}
	// 与“同一份样品内部测量项目重复”的区分由两条措辞的直接对比在
	// TestOpenDuplicateSampleIDMessageDistinctFromDuplicateItem 中验证。
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// writeRawDataFile 直接写入原始 JSON，用于构造 Go map 无法表达的重复键文件。
func writeRawDataFile(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

func sampleObjectJSON(t *testing.T, smp *Sample) string {
	t.Helper()
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	return string(b)
}

// rawEntry 拼出一条 samples 集合条目，keyText 是文件中的原始键标记，
// 例如 strconv.Quote("S1") 或字面量 `"S1"`。
func rawEntry(t *testing.T, keyText string, smp *Sample) string {
	t.Helper()
	return keyText + ":" + sampleObjectJSON(t, smp)
}

func rawStoreWithEntries(t *testing.T, entries []string) string {
	t.Helper()
	pts, err := json.Marshal(openPoints())
	if err != nil {
		t.Fatalf("marshal points: %v", err)
	}
	return fmt.Sprintf(`{"points":%s,"samples":{%s}}`, pts, strings.Join(entries, ","))
}

// dupExceededS1：pH 测量值 9、上限 8、已确认超标。
func dupExceededS1() *Sample {
	return &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusConfirmed, Exceeded: true,
		Results: []ItemResult{
			{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
		},
	}
}

// dupCompliantS1：同一采样点、同一采样时间，pH 测量值 7、上限 8、已确认达标。
func dupCompliantS1() *Sample {
	return &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusConfirmed, Exceeded: false,
		Results: []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		},
	}
}

// 任务中的核心场景：同一点同一时刻的 S1 先存已确认超标（9 > 8），后一条 S1
// 存已确认达标（7 ≤ 8），中间还隔着另一份样品。不能按排列顺序把后一条当成
// “最近有效结果”返回：整次打开失败、nil 存放，且最近结果查询无从进行。
func TestOpenDuplicateSampleIDExceededThenCompliant(t *testing.T) {
	dir := t.TempDir()
	other := &Sample{
		ID: "S-OTHER", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 5}},
		Status:       StatusPending,
	}
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S-OTHER"), other),
		rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")

	// 不能留下任何内部状态：修正成只保留超标那一条后，同目录即可正常打开，
	// 且读回的是超标结论（证明此前没有悄悄读入达标版本）。
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S-OTHER"), other),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("去除重复编号后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	if !latest.Exceeded || latest.Results[0].Value != 9 || latest.Results[0].Limit != 8 {
		t.Fatalf("已确认的超标依据必须原样保留: %+v", latest)
	}
}

// 即使两条重复记录内容完全相同，也按重复编号拒绝：不能合并、不能择一返回。
func TestOpenDuplicateSampleIDIdenticalRecords(t *testing.T) {
	dir := t.TempDir()
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 编号是否相同按 JSON 解码后的完整文本判断：一处直接写 "S1"，另一处把 S 写成
// Unicode 转义（"S1" 解码后仍是 S1），必须识别为重复。
func TestOpenDuplicateSampleIDUnicodeEscape(t *testing.T) {
	dir := t.TempDir()
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, `"S1"`, dupExceededS1()),
		// 文件中的原始键标记是 Unicode 转义写出的 S（\u0053）加字面量 1，
		// 而不是直接的 S1；Go 原始字符串字面量里反斜杠按原样落盘。
		rawEntry(t, `"\u00531"`, dupCompliantS1()),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 重复不依赖两条记录相邻：S1、S2 之后再次出现 S1，仍要识别。
func TestOpenDuplicateSampleIDNonAdjacent(t *testing.T) {
	dir := t.TempDir()
	s2 := &Sample{
		ID: "S2", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 5}},
		Status:       StatusPending,
	}
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S2"), s2),
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 唯一性要求对任何状态的样品都生效，不以是否已有判定结果为条件。
func TestOpenDuplicateSampleIDAppliesToAllStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status Status
	}{
		{"待判定", StatusPending},
		{"已确认", StatusConfirmed},
		{"已作废", StatusVoided},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			smp := &Sample{
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       c.status,
			}
			if c.status == StatusConfirmed {
				smp.Exceeded = true
				smp.Results = []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				}
			}
			if c.status == StatusVoided {
				smp.VoidReason = "录入信息有误"
			}
			raw := rawStoreWithEntries(t, []string{
				rawEntry(t, strconv.Quote("S1"), smp),
				rawEntry(t, strconv.Quote("S1"), smp),
			})
			writeRawDataFile(t, dir, raw)
			assertOpenRejectsDuplicateSampleID(t, dir, "S1")
		})
	}
}

// 文件里其他样品再正常，也不能只读入正常部分：重复编号让整次打开失败，
// 正常样品也读不到。
func TestOpenDuplicateSampleIDRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := &Sample{
		ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusConfirmed, Exceeded: false,
		Results: []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		},
	}
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S-GOOD"), good),
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 样品集合内编号重复的错误信息必须能与同一份样品内部测量项目重复区分开。
func TestOpenDuplicateSampleIDMessageDistinctFromDuplicateItem(t *testing.T) {
	dir := t.TempDir()

	// 样品集合层：两个 S1。
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
	}))
	_, err := Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("编号重复应返回 ErrCorruptRecord，got %v", err)
	}
	idMsg := err.Error()
	if !strings.Contains(idMsg, "样品集合中存在重复的样品编号") {
		t.Fatalf("编号重复应说明样品集合中存在重复编号，实际 %q", idMsg)
	}

	// 单份样品内部：measurements 里 pH 出现两次，只有一个 S1。
	within := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}, {Item: openPH, Value: 7}},
		Status:       StatusPending,
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), within),
	}))
	_, err = Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("测量项目重复应返回 ErrCorruptRecord，got %v", err)
	}
	itemMsg := err.Error()
	if !strings.Contains(itemMsg, "测量项目") {
		t.Fatalf("测量项目重复应点名测量项目，实际 %q", itemMsg)
	}
	if strings.Contains(itemMsg, "样品集合中存在重复的样品编号") {
		t.Fatalf("测量项目重复不应报成样品集合内编号重复，实际 %q", itemMsg)
	}
}

// 不同样品都包含 pH（或各自包含同名字段）是正常数据，不能误判为编号重复。
func TestOpenSameItemAcrossSamplesIsNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	a := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	b := &Sample{
		ID: "S2", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusPending,
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), a),
		rawEntry(t, strconv.Quote("S2"), b),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同样品各含 pH 是正常数据: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份不同编号的样品都应读入: %+v err=%v", list, err)
	}
}

// 同一份样品记录内部出现重名键（这里重复写 id），不是样品集合层的编号重复：
// 扫描必须跳过整条记录值，不把记录内部字段当成样品编号统计。
func TestOpenDuplicateKeyInsideSampleRecordIsNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	smp := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	obj := sampleObjectJSON(t, smp)
	// 去掉末尾 '}'，再补一个同值的重复 id 字段。
	obj = obj[:len(obj)-1] + `,"id":"S1"}`
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		`"S1":` + obj,
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("记录内部重名键不是样品编号重复，其余内容完整时应正常读入: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("样品应正常读入: %+v err=%v", list, err)
	}
}

// 空样品集合继续照常打开。
func TestOpenEmptySamplesNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points:  openPoints(),
		Samples: map[string]*Sample{},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("空样品集合应正常打开: %v", err)
	}
	defer s.Close()
}

// 正常录入的幂等行为不因这次修复改变：同编号同内容返回原样品；
// 编号相同但内容不同仍由录入接口整体拒绝（不在打开阶段处理正常流程文件）。
func TestOpenResubmitBehaviorUnchangedAfterDuplicateIDFix(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	first := mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	again, err := s.SubmitSample("S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	if err != nil || again.ID != first.ID {
		t.Fatalf("同编号同内容应返回原样品: %+v err=%v", again, err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 7}); !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("同编号内容不同仍应整体拒绝 ErrSampleConflict，got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 正常流程保存的文件重新打开不受影响。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的文件应正常打开: %v", err)
	}
	defer s2.Close()
	if list, err := s2.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("重开后应看到原样品: %+v err=%v", list, err)
	}
}
