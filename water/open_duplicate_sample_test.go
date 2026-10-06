package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对样品集合编号唯一性的核对：
// samples 中同一个样品编号只能出现一次。直接解进映射表时，JSON 同名键会被
// 后一条记录静默覆盖，先保存的已确认超标结论可能被后一条达标结论顶替，而
// 映射表中只剩一条，逐份样品的完整性检查发现不了被覆盖的记录。因此 Open 必须
// 在逐条解码样品之前扫描样品集合的原始 JSON：
// 发现重复编号即整次失败，返回 nil 存放，错误可被 errors.Is(err, ErrCorruptRecord)
// 识别，信息指出重复的样品编号并说明是样品集合中存在重复编号（区别于同一份
// 样品内部测量项目重复），原文件字节不变；不删除条目、不合并内容、不择一保留、
// 不补出判定或重算历史结论，文件里其他样品再正常也不部分读入。
// 编号按 JSON 解码后的完整文本判断（Unicode 转义归一），重复条目不相邻也一样；
// 即使两条内容完全相同也算重复；这条要求对 pending、confirmed、voided 全部
// 状态生效，不以是否已有判定结果为条件。不同样品各自包含同名测量项目（如都有
// pH）与样品集合的键无关，不能被误判。空样品集合照常打开。

// assertOpenRejectsDuplicateSample 断言打开因样品集合编号重复而整次失败：
// 返回 nil 存放、错误为 ErrCorruptRecord，信息点名重复编号并说明样品集合中
// 存在重复编号（且措辞能与同一份样品内部测量项目重复区分），原文件字节不变。
func assertOpenRejectsDuplicateSample(t *testing.T, dir, sampleID string) {
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
		t.Fatalf("样品集合中的重复编号 %s 必须让整次 Open 失败", sampleID)
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
	if !strings.Contains(msg, "重复编号") {
		t.Fatalf("错误信息应说明样品集合中存在重复编号，实际 %q", msg)
	}
	if !strings.Contains(msg, "样品集合") {
		t.Fatalf("错误信息应说明问题出在样品集合，以区别于单份样品内部的项目重复，实际 %q", msg)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// openDupHeader 构造数据文件开头：一个采样点加样品集合的左花括号。
// 已确认样品保存的逐项依据只核对记录自身，不需要文件里另有登记限值。
func openDupHeader() string {
	return `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {`
}

const openDupFooter = `
  }
}`

func openDupPendingRecord(value string) string {
	return `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": ` + value + `}],
      "status": "pending"
    }`
}

// 同一点同一采样时间的 S1 写了两条：前一条保存 pH 9、上限 8 的已确认超标
// 结论，后一条保存 pH 7、上限 8 的已确认达标结论，中间还隔着另一份样品 S2。
// 不能按排列顺序把后一条当成“最近有效结果”：整次 Open 必须失败。
func TestOpenDuplicateSampleIDExceededThenCompliant(t *testing.T) {
	dir := t.TempDir()
	exceeded := `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [
        {"item": "pH", "value": 9, "limit": 8, "limitEffective": "` + openEff().Format("2006-01-02T15:04:05Z07:00") + `", "exceeded": true}
      ]
    }`
	compliant := `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [
        {"item": "pH", "value": 7, "limit": 8, "limitEffective": "` + openEff().Format("2006-01-02T15:04:05Z07:00") + `", "exceeded": false}
      ]
    }`
	other := `{
      "id": "S2", "pointId": "P1", "sampledAt": "` + at(11, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 5}],
      "status": "pending"
    }`
	raw := openDupHeader() + `
    "S1": ` + exceeded + `,
    "S2": ` + other + `,
    "S1": ` + compliant + openDupFooter
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsDuplicateSample(t, dir, "S1")
}

// 编号按 JSON 解码后的完整文本判断：一个键直接写 "S1"，另一个把字符写成
// Unicode 转义（1 是 1、S 是 S），仍是同一编号。
// 用 Go 原始字符串拼 JSON，保证反斜杠原样进入文件，由 JSON 解码器转义。
func TestOpenDuplicateSampleIDUnicodeEscape(t *testing.T) {
	for _, escapedKey := range []string{`"S\u0031"`, `"\u00531"`} {
		dir := t.TempDir()
		raw := openDupHeader() + `
    "S1": ` + openDupPendingRecord("9") + `,
    ` + escapedKey + `: ` + openDupPendingRecord("7") + openDupFooter
		writeRawDiskFile(t, dir, raw)
		assertOpenRejectsDuplicateSample(t, dir, "S1")
	}
}

// 即使两条重复记录的内容完全相同，也按重复编号拒绝：不合并、不当成同一条放行；
// 两条之间隔着另一份样品也不影响识别。
func TestOpenDuplicateSampleIDIdenticalRecords(t *testing.T) {
	dir := t.TempDir()
	record := openDupPendingRecord("9")
	other := `{
      "id": "S2", "pointId": "P1", "sampledAt": "` + at(11, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 5}],
      "status": "pending"
    }`
	raw := openDupHeader() + `
    "S1": ` + record + `,
    "S2": ` + other + `,
    "S1": ` + record + openDupFooter
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsDuplicateSample(t, dir, "S1")
}

// 编号唯一性不以是否已有判定结果为条件：两条待判定、两条已作废同样拒绝。
func TestOpenDuplicateSampleIDAppliesToAllStatuses(t *testing.T) {
	voided := func(value string) string {
		return `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": ` + value + `}],
      "status": "voided", "voidReason": "录入信息有误"
    }`
	}
	cases := []struct {
		name   string
		record func(string) string
	}{
		{"待判定", openDupPendingRecord},
		{"已作废", voided},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := openDupHeader() + `
    "S1": ` + c.record("9") + `,
    "S1": ` + c.record("7") + openDupFooter
			writeRawDiskFile(t, dir, raw)
			assertOpenRejectsDuplicateSample(t, dir, "S1")
		})
	}
}

// 文件里其他样品完全正常，也不能只读入正常部分：重复编号让整次打开失败，
// 正常样品同样不可查询；原文件保持原样。
func TestOpenDuplicateSampleIDRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := `{
      "id": "S-GOOD", "pointId": "P1", "sampledAt": "` + at(5, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [
        {"item": "pH", "value": 7, "limit": 8, "limitEffective": "` + openEff().Format("2006-01-02T15:04:05Z07:00") + `", "exceeded": false}
      ]
    }`
	raw := openDupHeader() + `
    "S-GOOD": ` + good + `,
    "S1": ` + openDupPendingRecord("9") + `,
    "S1": ` + openDupPendingRecord("7") + openDupFooter
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsDuplicateSample(t, dir, "S1")
}

// 重复编号的核对在逐条样品校验之前：第二条 S1 即使本身还缺状态（残缺记录），
// 也要先报样品集合编号重复，而不是被残缺记录的错误顶替。
func TestOpenDuplicateSampleIDCheckedBeforePerRecordValidation(t *testing.T) {
	dir := t.TempDir()
	missingStatus := `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 7}]
    }`
	raw := openDupHeader() + `
    "S1": ` + openDupPendingRecord("9") + `,
    "S1": ` + missingStatus + openDupFooter
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsDuplicateSample(t, dir, "S1")
}

// 打开因重复编号失败后不留内部状态：在文件外删掉重复条目覆盖写回，同一目录
// 即可正常打开，保留下来的样品与已保存判定依据照常可见、不被重算。
func TestOpenDuplicateSampleIDRejectionLeavesNoState(t *testing.T) {
	dir := t.TempDir()
	exceeded := `{
      "id": "S1", "pointId": "P1", "sampledAt": "` + at(10, 0).Format("2006-01-02T15:04:05Z07:00") + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [
        {"item": "pH", "value": 9, "limit": 8, "limitEffective": "` + openEff().Format("2006-01-02T15:04:05Z07:00") + `", "exceeded": true}
      ]
    }`
	bad := openDupHeader() + `
    "S1": ` + exceeded + `,
    "S1": ` + openDupPendingRecord("7") + openDupFooter
	writeRawDiskFile(t, dir, bad)
	if got, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || got != nil {
		t.Fatalf("重复编号必须拒绝，got=%v err=%v", got, err)
	}

	// 在文件外修正：只保留已确认超标那一条。
	writeRawDiskFile(t, dir, openDupHeader()+`
    "S1": `+exceeded+openDupFooter)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修正为唯一编号后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("修正后应能看到原样品及其结论: %+v ok=%v err=%v", latest, ok, err)
	}
	if r := latest.Results[0]; r.Value != 9 || r.Limit != 8 || !r.Exceeded || !latest.Exceeded {
		t.Fatalf("已保存的超标依据必须原样保留: %+v", r)
	}
}

// 不同样品都包含 pH、其余字段名也相同，是正常数据：样品编号不同即各自读入，
// 不能被误判成样品编号重复。
func TestOpenDifferentSamplesWithSameFieldsAreNotDuplicates(t *testing.T) {
	dir := t.TempDir()
	s2 := strings.Replace(openDupPendingRecord("9"), `"id": "S1"`, `"id": "S2"`, 1)
	raw := openDupHeader() + `
    "S1": ` + openDupPendingRecord("9") + `,
    "S2": ` + s2 + openDupFooter
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同样品编号、相同测量项目与字段是正常数据: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应读入: %+v err=%v", list, err)
	}
}

// 正常流程保存的文件不会有重复编号：同编号同内容再次提交仍返回原样品，
// 重新打开照常读入，已保存的判定依据与作废状态不因这次修复改变。
func TestOpenNormalResubmitFileHasUniqueIDs(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8, openEff())
	first := mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	again, err := s.SubmitSample("S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	if err != nil || again.ID != first.ID {
		t.Fatalf("同编号同内容应返回原样品: %+v err=%v", again, err)
	}
	conf, err := s.Confirm("S1")
	if err != nil || !conf.Exceeded {
		t.Fatalf("setup confirm: %+v err=%v", conf, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的文件应照常打开: %v", err)
	}
	defer s2.Close()
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" || !latest.Exceeded {
		t.Fatalf("重开后结论应原样保留: %+v ok=%v err=%v", latest, ok, err)
	}
}
