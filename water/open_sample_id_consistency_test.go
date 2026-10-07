package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对样品编号本身的核对：每份样品在 samples 集合中
// 的集合编号必须与记录自身 id 字段逐字一致，并沿用正常录入后保存的编号形式
// （不含首尾空白）。id 缺失、为 null、为空字符串或仅含空白都算记录缺少编号；
// 任一处带首尾空白也不能整理后放行；两处文本都完整但对不上则整次打开失败。
// 比较按 JSON 解码后的文本进行，不转大小写、不替换字符：一处直接写 S1、另一处
// 用 Unicode 转义表示同一文本可以接受；S1 与 s1、S1 与 S2 则不一致。待判定、
// 已确认、已作废样品一律适用，测量与判定依据完整不能代替编号正确，同一文件里
// 其他样品正常也不能只读入正常部分。错误可识别为 ErrCorruptRecord、返回 nil
// 存放，信息指出集合中的编号并区分缺少编号、编号含首尾空白与两处编号不一致
// （不一致时同时带出两处编号），且与样品集合内编号重复的错误可区分；原文件
// 字节不变。正常录入保存的数据照常打开，查询、排序与作废资格不变。

// assertFileUnchanged 断言一次失败的 Open 没有回写数据文件。
func assertFileUnchanged(t *testing.T, dir string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file after open: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// assertOpenIDCorrupt 打开并断言：整次失败、nil 存放、ErrCorruptRecord，
// 错误信息包含所有 want 子串、且不包含任一 notWant 子串，原文件字节不变。
func assertOpenIDCorrupt(t *testing.T, dir string, want, notWant []string) {
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
		t.Fatalf("编号有问题的记录必须让整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Fatalf("错误信息应包含 %q，实际 %q", w, msg)
		}
	}
	// 哨兵错误自身的说明里也带有“不一致”“缺少状态”等字样，因此反向断言只
	// 针对去掉哨兵前缀后的具体原因（与 open_missing_sampled_at_test.go 同款）。
	detail := strings.TrimPrefix(msg, ErrCorruptRecord.Error())
	for _, nw := range notWant {
		if strings.Contains(detail, nw) {
			t.Fatalf("具体错误原因不应包含 %q，实际 %q", nw, detail)
		}
	}
	assertFileUnchanged(t, dir, before)
}

// idCheckSample 构造一份除编号外内容完整的样品，便于证明测量与判定依据完整
// 不能代替编号正确。status 决定记录形态：confirmed 带完整达标判定，voided
// 带作废原因（待判定后直接作废、无历史依据）。
func idCheckSample(id string, status Status) *Sample {
	smp := &Sample{
		ID:           id,
		PointID:      "P1",
		SampledAt:    at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       status,
	}
	switch status {
	case StatusConfirmed:
		smp.Exceeded = false
		smp.Results = []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		}
	case StatusVoided:
		smp.VoidReason = "录入信息有误"
	}
	return smp
}

// sampleJSONWithIDToken 把样品序列化后，将其中的 "id":"..." 字段整体替换成
// idToken 给出的原始 JSON 片段，用于构造字段缺失、null、空串或 Unicode 转义
// 等 Go 结构体表达不出的写法。idToken 为空串表示整个字段缺失（连逗号一起去掉）。
// 调用方须保证 smp.ID 非空且为 "S1"，且 id 是序列化后的第一个字段。
func sampleJSONWithIDToken(t *testing.T, smp *Sample, idToken string) string {
	t.Helper()
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	s := string(b)
	if idToken == "" {
		old := `"id":"` + smp.ID + `",`
		if !strings.Contains(s, old) {
			t.Fatalf("序列化结果中找不到编号字段 %q：%s", old, s)
		}
		return strings.Replace(s, old, "", 1)
	}
	old := `"id":"` + smp.ID + `"`
	if !strings.Contains(s, old) {
		t.Fatalf("序列化结果中找不到编号字段 %q：%s", old, s)
	}
	return strings.Replace(s, old, idToken, 1)
}

// rawEntryWithObject 用给定的对象文本拼出一条 samples 集合条目。
func rawEntryWithObject(keyText, objectJSON string) string {
	return keyText + ":" + objectJSON
}

// 任务中的核心场景：记录放在集合的 S1 条目下，记录里的 id 却写成 S2，
// 其余内容完整无缺也必须整次拒绝；信息同时带出两处编号。
func TestOpenCollectionKeyMismatchesRecordID(t *testing.T) {
	dir := t.TempDir()
	smp := idCheckSample("S2", StatusPending)
	raw := rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), smp),
	})
	writeRawDataFile(t, dir, raw)
	assertOpenIDCorrupt(t, dir,
		[]string{"S1", "S2", "不一致"},
		[]string{"样品集合中存在重复的样品编号"})
}

// 两处编号不一致的拒绝信息必须与样品集合内编号重复的拒绝信息区分开。
func TestOpenIDMismatchMessageDistinctFromDuplicate(t *testing.T) {
	dir := t.TempDir()
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), idCheckSample("S2", StatusPending)),
	}))
	_, err := Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	if strings.Contains(err.Error(), "样品集合中存在重复的样品编号") {
		t.Fatalf("两处编号不一致不应报成集合内编号重复：%v", err)
	}
}

// 编号核对先于其他逐份检查：id 对不上时即使状态也有问题，也必须先报两处编号
// 不一致，而不是让缺状态等问题抢先报出。
func TestOpenIDMismatchReportedBeforeOtherDamage(t *testing.T) {
	dir := t.TempDir()
	// id 写成 S2，同时去掉 status 字段：应先报编号对不上。
	obj := sampleJSONWithIDToken(t, idCheckSample("S2", StatusPending), `"id":"S2"`)
	obj = strings.Replace(obj, `,"status":"pending"`, "", 1)
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntryWithObject(`"S1"`, obj),
	}))
	assertOpenIDCorrupt(t, dir,
		[]string{"S1", "S2", "不一致"},
		[]string{"缺少状态"})
}

// id 字段的各种缺失写法都算记录缺少编号：字段缺失、null、空字符串、仅含空白。
func TestOpenRecordMissingIDForms(t *testing.T) {
	cases := []struct {
		name    string
		idToken string
	}{
		{"字段缺失", ""},
		{"null", `"id":null`},
		{"空字符串", `"id":""`},
		{"仅含空格", `"id":"   "`},
		{"仅含制表与换行", `"id":"\t\n "`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			obj := sampleJSONWithIDToken(t, idCheckSample("S1", StatusPending), c.idToken)
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(`"S1"`, obj),
			}))
			assertOpenIDCorrupt(t, dir,
				[]string{"S1", "缺少编号"},
				[]string{"不一致", "样品集合中存在重复的样品编号"})
		})
	}
}

// 整份记录为 JSON null 时没有 id 字段，按记录缺少编号处理，并按集合编号点名。
func TestOpenNullRecordReportedAsMissingID(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": nil,
		},
	})
	assertOpenIDCorrupt(t, dir,
		[]string{"S1", "缺少编号"},
		[]string{"不一致"})
}

// 缺少编号的要求对三种状态都生效；测量与判定依据完整不能代替编号正确。
func TestOpenMissingIDAppliesToAllStatuses(t *testing.T) {
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
			obj := sampleJSONWithIDToken(t, idCheckSample("S1", c.status), "")
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(`"S1"`, obj),
			}))
			assertOpenIDCorrupt(t, dir, []string{"S1", "缺少编号"}, nil)
		})
	}
}

// 两处编号不一致的要求同样对三种状态生效，已确认样品的判定依据完整也不放行。
func TestOpenIDMismatchAppliesToAllStatuses(t *testing.T) {
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
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntry(t, strconv.Quote("S1"), idCheckSample("S2", c.status)),
			}))
			assertOpenIDCorrupt(t, dir, []string{"S1", "S2", "不一致"}, nil)
		})
	}
}

// 任一处编号带首尾空白都不能整理后放行，即使两处去掉空白后相同；分别覆盖
// 只有集合键带空白、只有记录 id 带空白、两处都带空白三种情况。
func TestOpenIDWithSurroundingWhitespaceRejected(t *testing.T) {
	cases := []struct {
		name    string
		keyText string
		idToken string
	}{
		{"集合键带空白", `" S1 "`, `"id":"S1"`},
		{"记录id带空白", `"S1"`, `"id":" S1 "`},
		{"两处都带空白", `" S1 "`, `"id":" S1 "`},
		{"仅末尾制表符", `"S1\t"`, `"id":"S1\t"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			obj := sampleJSONWithIDToken(t, idCheckSample("S1", StatusPending), c.idToken)
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(c.keyText, obj),
			}))
			assertOpenIDCorrupt(t, dir,
				[]string{"含首尾空白"},
				[]string{"不一致"})
		})
	}
}

// 编号按 JSON 解码后的文本逐字比较，不转换大小写：S1 与 s1 对不上。
func TestOpenIDComparisonIsCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), idCheckSample("s1", StatusPending)),
	}))
	assertOpenIDCorrupt(t, dir, []string{"S1", "s1", "不一致"}, nil)
}

// Unicode 转义只是 JSON 的另一种写法：集合键与记录 id 任一处用 S1
// 表示 S1，解码后文本相同，必须照常读入。用双引号字符串加双反斜杠，
// 使落盘文本里真的出现 \u0053 这六个字符。
func TestOpenUnicodeEscapeSameTextAccepted(t *testing.T) {
	escS1 := "\"\\u00531\"" // 文件里的键标记 "\u00531"，解码后是 S1
	escID := "\"id\":\"\\u00531\""
	dirS1 := "\"S1\"" // 文件里直接写 "S1"
	dirID := "\"id\":\"S1\""
	cases := []struct {
		name    string
		keyText string
		idToken string
	}{
		{"键用转义", escS1, dirID},
		{"id用转义", dirS1, escID},
		{"两处都用转义", escS1, escID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			obj := sampleJSONWithIDToken(t, idCheckSample("S1", StatusPending), c.idToken)
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(c.keyText, obj),
			}))
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Unicode 转义表示同一文本时应正常读入: %v", err)
			}
			defer s.Close()
			list, err := s.ListByPoint("P1")
			if err != nil || len(list) != 1 || list[0].ID != "S1" {
				t.Fatalf("应读入编号 S1 的样品: %+v err=%v", list, err)
			}
		})
	}
}

// 中文与编号内部的合法字符（含内部空白）仍可使用，只要两处文本逐字相同。
func TestOpenChineseAndInternalWhitespaceAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"样品一":    idCheckSample("样品一", StatusPending),
			"S 1\t中": idCheckSample("S 1\t中", StatusPending),
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("中文与编号内部合法字符应正常读入: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应读入: %+v err=%v", list, err)
	}
}

// 文件里其他样品再正常，也不能只读入正常部分：编号对不上让整次打开失败。
// 修正编号后同目录即可正常打开，且正常样品当时没有被悄悄改动。
func TestOpenIDProblemRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := &Sample{
		ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusConfirmed, Exceeded: false,
		Results: []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		},
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S-GOOD"), good),
		rawEntry(t, strconv.Quote("S1"), idCheckSample("S2", StatusPending)),
	}))
	assertOpenIDCorrupt(t, dir, []string{"S1", "S2", "不一致"}, nil)

	// 去掉对不上的记录后，正常样品原样可读：证明失败时没有留下部分读入状态。
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S-GOOD"), good),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修正编号问题后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("最近有效结果应指向未被改动的正常样品: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 空样品集合与缺失样品集合的已有数据照常打开，不要求尚未录入样品的数据已有编号。
func TestOpenEmptyOrMissingSamplesStillOpens(t *testing.T) {
	t.Run("空集合", func(t *testing.T) {
		dir := t.TempDir()
		writeDiskFile(t, dir, diskState{Points: openPoints(), Samples: map[string]*Sample{}})
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("空样品集合应正常打开: %v", err)
		}
		s.Close()
	})
	t.Run("集合缺失", func(t *testing.T) {
		dir := t.TempDir()
		writeDiskFile(t, dir, diskState{Points: openPoints()})
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("缺失样品集合应正常打开: %v", err)
		}
		s.Close()
	})
}

// 正常录入保存的数据两处编号始终一致：录入时带首尾空白会被整理，保存与重开后
// 编号、按点列表、最近有效结果、排序与作废资格都保持原有行为。
func TestOpenNormalFlowUnchangedAfterIDCheck(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8, openEff())
	// 录入时编号两侧带空白：正常流程整理为 S1 后保存。
	mustSample(t, s, " S1 ", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	confirmed := mustConfirm(t, s, "S1")
	if confirmed.ID != "S1" || !confirmed.Exceeded {
		t.Fatalf("确认结果应保留整理后的编号与超标结论: %+v", confirmed)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重新打开：编号、上限、历史结论与排序不变。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的文件应正常打开: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("按点列表应保留原样品 S1: %+v err=%v", list, err)
	}
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应保留 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	if len(latest.Results) != 1 || latest.Results[0].Limit != 8 || !latest.Results[0].Exceeded {
		t.Fatalf("历史判定依据必须原样保留: %+v", latest)
	}

	// 作废后重新打开：作废资格不变，最近有效结果跳过它。
	if _, err := s2.Void("S1", "录入信息有误"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("作废后重新打开应正常: %v", err)
	}
	defer s3.Close()
	if _, ok, err := s3.LatestResult("P1"); err != nil || ok {
		t.Fatalf("已作废样品重开后仍应被最近有效结果跳过: ok=%v err=%v", ok, err)
	}
	if list, err := s3.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].Status != StatusVoided {
		t.Fatalf("作废记录与按点列表必须原样保留: %+v err=%v", list, err)
	}
}
