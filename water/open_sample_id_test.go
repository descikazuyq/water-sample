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

// 本文件保护“打开本地数据”时对单份样品两处编号的核对：
// 每份样品在 samples 集合中的编号（集合键）必须与记录自身的 id 完全一致，
// 并沿用正常录入后保存的编号形式。当前一份记录放在 S1 条目下、记录 id 却写成
// S2 时仍能打开，按点查看给出 S2，拿它确认或作废可能得到样品不存在，也可能
// 落到另一份真正编号为 S2 的记录上；id 缺失时查询还会返回空编号。这类记录必须
// 在打开时整次被拒绝：errors.Is(err, ErrCorruptRecord)、返回 nil 存放、原文件
// 字节不变，同一文件里其他样品正常也不能只读入正常部分。
//
// id 缺失、为 null、为空字符串或仅含空白都算记录缺少编号；集合键或记录 id
// 任一处带首尾空白都不能整理后放行；两处都完整后按 JSON 解码后的文本逐字比较，
// 不转换大小写、不替换字符：一处直接写 S1、另一处用 Unicode 转义表示同一文本
// 可以接受，S1 与 s1、S1 与 S2 则不一致；中文与编号内部的合法字符仍可使用。
// 这三种问题各有不同错误信息，不一致时同时带出集合编号与记录 id；它们与样品
// 集合层的同编号重复是两种不同错误。正常录入保存的数据照常打开。

// assertOpenRejectsID 断言打开因单份记录两处编号问题而整次失败：nil 存放、
// ErrCorruptRecord、原文件字节不变，且信息指出集合中的编号。
// wantSubs 是信息中必须同时出现的片段；forbid 是不得出现的片段。
func assertOpenRejectsID(t *testing.T, dir, keyHint string, wantSubs, forbid []string) {
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
		t.Fatalf("两处编号有问题的记录必须让整次 Open 失败，集合编号=%s", keyHint)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, keyHint) {
		t.Fatalf("错误信息应指出集合中的编号 %q，实际 %q", keyHint, msg)
	}
	for _, sub := range wantSubs {
		if !strings.Contains(msg, sub) {
			t.Fatalf("错误信息应包含 %q，实际 %q", sub, msg)
		}
	}
	for _, sub := range forbid {
		if strings.Contains(msg, sub) {
			t.Fatalf("错误信息不应包含 %q，实际 %q", sub, msg)
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

// sampleObjectWithID 把 smp 序列化成 JSON 对象后，把 id 成员替换成 idToken
// （如 "S2"、null）；omitID 为 true 时整个 id 成员缺失。键序变化无所谓，
// 读取只按字段名识别。
func sampleObjectWithID(t *testing.T, smp *Sample, idToken string, omitID bool) string {
	t.Helper()
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal sample to map: %v", err)
	}
	if omitID {
		delete(m, "id")
	} else {
		m["id"] = json.RawMessage(idToken)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("remarshal sample: %v", err)
	}
	return string(out)
}

// rawIDEntry 拼出 samples 集合中的一条：键与记录 id 都按原始 JSON 文本给出，
// 用于构造 Unicode 转义等 Go map 无法直接表达的写法。
func rawIDEntry(keyText, objJSON string) string { return keyText + ":" + objJSON }

// 落盘字节 "\u00531"：这是 JSON 字符串 "S1" 把 S 写成 Unicode 转义的写法，
// JSON 解码后仍是文本 S1。Go 解释字符串里要落出反斜杠得写成 \\u。
const (
	escapedS1Token = "\"\\u00531\""
	escapedS1Key   = "\"\\u00531\""
)

// mismatchedS1 是一份内容完整、但记录 id 与所在集合键对不上的样品：
// 放在集合键 S1 下，记录自身 id 却写成 S2。
func mismatchedS1(status Status) *Sample {
	smp := &Sample{
		ID: "S2", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       status,
	}
	switch status {
	case StatusConfirmed:
		smp.Exceeded = true
		smp.Results = []ItemResult{
			{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
		}
	case StatusVoided:
		smp.VoidReason = "录入信息有误"
	}
	return smp
}

// 任务中的核心场景：记录放在 S1 条目下、id 却写成 S2，三种状态都必须让
// 整次 Open 失败；错误信息同时带出两处编号并说明不一致。
func TestOpenCollectionKeyMismatchesRecordID(t *testing.T) {
	for _, status := range []Status{StatusPending, StatusConfirmed, StatusVoided} {
		t.Run(string(status), func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": mismatchedS1(status),
				},
			})
			assertOpenRejectsID(t, dir, "S1",
				[]string{"S2", "不一致", "样品集合编号", "记录自身编号"},
				[]string{"样品集合中存在重复的样品编号"})
		})
	}
}

// 已确认样品测量与判定依据完整，也不能代替编号正确：编号对不上仍整次拒绝。
func TestOpenMismatchedIDRejectsDespiteCompleteBasis(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S2", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: phTurbResults(),
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"S2", "不一致"}, nil)
}

// id 字段在文件中整个缺失：不能读成空编号，整次打开失败，错误说明字段缺失。
func TestOpenRecordIDFieldMissing(t *testing.T) {
	dir := t.TempDir()
	smp := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawIDEntry(strconv.Quote("S1"), sampleObjectWithID(t, smp, "", true)),
	}))
	assertOpenRejectsID(t, dir, "S1",
		[]string{"缺少编号", "字段缺失或为 null"}, nil)
}

// id 保存为 JSON null：与字段缺失同一类问题，同样整次拒绝。
func TestOpenRecordIDNull(t *testing.T) {
	dir := t.TempDir()
	smp := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawIDEntry(strconv.Quote("S1"), sampleObjectWithID(t, smp, "null", false)),
	}))
	assertOpenRejectsID(t, dir, "S1",
		[]string{"缺少编号", "字段缺失或为 null"}, nil)
}

// id 明确写为空字符串：属于缺少编号，但要与字段缺失/null 的说法区分开。
func TestOpenRecordIDEmptyString(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"缺少编号", "空字符串或仅含空白"}, []string{"字段缺失或为 null"})
}

// id 仅含空白（空格、制表符、换行）同样是缺少编号。
func TestOpenRecordIDWhitespaceOnly(t *testing.T) {
	for _, id := range []string{" ", "\t", " \n\t ", "　"} {
		t.Run("blank="+strconv.Quote(id), func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": {
						ID: id, PointID: "P1", SampledAt: at(10, 0),
						Measurements: []Measurement{{Item: openPH, Value: 9}},
						Status:       StatusPending,
					},
				},
			})
			assertOpenRejectsID(t, dir, "S1",
				[]string{"缺少编号", "空字符串或仅含空白"}, []string{"字段缺失或为 null"})
		})
	}
}

// 记录 id 去掉首尾空白后与集合键相同也不能整理后放行：必须报编号含首尾空白。
func TestOpenRecordIDWithSurroundingWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "  S1\t", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"首尾空白"}, []string{"不一致：", "缺少编号"})
}

// 集合键带首尾空白、记录 id 干净，同样不能去空白后当作一致：报编号含首尾空白。
func TestOpenCollectionKeyWithSurroundingWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			" S1 ": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"首尾空白"}, []string{"不一致："})
}

// 两处都写成同一个带首尾空白的编号，也不能整理后放行。
func TestOpenBothKeyAndIDWithSurroundingWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			" S1": {
				ID: " S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1", []string{"首尾空白"}, nil)
}

// 大小写不同算不一致：比较不转换大小写，S1 与 s1 对不上。
func TestOpenIDMismatchCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "s1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"s1", "不一致"}, nil)
}

// 编号按 JSON 解码后的文本比较：集合键直接写 "S1"，记录 id 用 Unicode 转义
// 写出同一文本（S1 → S1），必须正常读入。
func TestOpenIDUnicodeEscapeSameTextAccepted(t *testing.T) {
	dir := t.TempDir()
	smp := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		// 文件中的 id 标记用 Unicode 转义写出 S（S）加字面量 1，
		// 解码后仍是 S1；转义序列用解释字符串字面量构造，保证落盘字节含反斜杠。
		rawIDEntry(`"S1"`, sampleObjectWithID(t, smp, escapedS1Token, false)),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Unicode 转义表示同一文本应正常读入: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("读入样品编号应为解码后的 S1: %+v err=%v", list, err)
	}
}

// 反过来：集合键用 Unicode 转义表示 S1，记录 id 直接写 S1，解码后同一文本，
// 同样正常读入。
func TestOpenCollectionKeyUnicodeEscapeSameTextAccepted(t *testing.T) {
	dir := t.TempDir()
	smp := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	// 连同 pH 上限一起写入，证明解码后的编号 S1 不仅能查到，还能正常确认。
	pts, _ := json.Marshal(openPoints())
	lims, _ := json.Marshal(map[string][]Limit{
		diskLimitKey("P1", openPH): {
			{PointID: "P1", Item: openPH, Value: 8, Effective: openEff()},
		},
	})
	raw := fmt.Sprintf(`{"points":%s,"limits":%s,"samples":{%s}}`,
		pts, lims, rawIDEntry(escapedS1Key, sampleObjectJSON(t, smp)))
	writeRawDataFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("集合键 Unicode 转义表示同一文本应正常读入: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("读入样品编号应为解码后的 S1: %+v err=%v", list, err)
	}
	conf, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("解码后的编号 S1 应可用于确认: %v", err)
	}
	if conf.ID != "S1" || !conf.Exceeded {
		t.Fatalf("按解码编号确认得到的结论异常: %+v", conf)
	}
}

// 中文编号和编号内部的合法字符（含内部空白、U+0000）照常使用：两处一致即读入。
func TestOpenIDWithChineseAndInternalCharsAccepted(t *testing.T) {
	for _, id := range []string{"样品1", "样品 1", "S\u00001"} {
		t.Run(strconv.Quote(id), func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					id: {
						ID: id, PointID: "P1", SampledAt: at(10, 0),
						Measurements: []Measurement{{Item: openPH, Value: 9}},
						Status:       StatusPending,
					},
				},
			})
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("两处一致的中文/内部字符编号应正常读入: %v", err)
			}
			defer s.Close()
			if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != id {
				t.Fatalf("编号必须原样保留: %+v err=%v", list, err)
			}
		})
	}
}

// 仅在内部字符上不同的两处编号（S1 vs S1 加 U+0000）也是不一致。
func TestOpenIDMismatchOnInternalChar(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1\x00", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1", []string{"不一致"}, nil)
}

// 一个文件里其他样品正常，也不能只读入正常部分：编号对不上让整次打开失败，
// 正常样品也读不到。
func TestOpenMismatchedIDRejectsWholeFile(t *testing.T) {
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
			// 集合键与记录 id 对不上的损坏样品。
			"S1": mismatchedS1(StatusPending),
		},
	})
	assertOpenRejectsID(t, dir, "S1", []string{"S2", "不一致"}, nil)
}

// 编号核对先于状态、采样时间等内容核对：编号缺失的记录即使同时缺状态，
// 也先报编号问题。
func TestOpenMissingIDReportedBeforeOtherDamage(t *testing.T) {
	dir := t.TempDir()
	// id 与 status 都缺失、采样时间也是零时间：仍应先报缺少编号。
	smp := &Sample{
		PointID:      "P1",
		Measurements: []Measurement{{Item: openPH, Value: 9}},
	}
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawIDEntry(strconv.Quote("S1"), sampleObjectWithID(t, smp, "", true)),
	}))
	assertOpenRejectsID(t, dir, "S1", []string{"缺少编号"},
		[]string{"缺少状态：", "缺少采样时间："})
}

// 单份记录两处编号不一致与样品集合层的同编号重复是两种不同错误，信息可区分。
func TestOpenIDMismatchDistinctFromDuplicateCollectionID(t *testing.T) {
	dir := t.TempDir()

	// 单份记录：集合键 S1、记录 id S2。
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": mismatchedS1(StatusPending),
		},
	})
	_, err := Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("编号不一致应返回 ErrCorruptRecord，got %v", err)
	}
	mismatchMsg := err.Error()
	if !strings.Contains(mismatchMsg, "不一致") || !strings.Contains(mismatchMsg, "S2") {
		t.Fatalf("编号不一致应说明两处编号不一致并带出记录 id，实际 %q", mismatchMsg)
	}
	if strings.Contains(mismatchMsg, "样品集合中存在重复的样品编号") {
		t.Fatalf("单份两处编号不一致不应报成集合层重复，实际 %q", mismatchMsg)
	}

	// 集合层重复：两个 S1，各自 id 也都是 S1。
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
	}))
	_, err = Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("集合层重复应返回 ErrCorruptRecord，got %v", err)
	}
	dupMsg := err.Error()
	if !strings.Contains(dupMsg, "样品集合中存在重复的样品编号") {
		t.Fatalf("集合层重复应明确报集合内编号重复，实际 %q", dupMsg)
	}
	if strings.Contains(dupMsg, "与记录自身编号") {
		t.Fatalf("集合层重复不应报成单份两处编号不一致，实际 %q", dupMsg)
	}
}

// 集合键与记录 id 不一致，但记录 id 恰好与同文件里另一份真正的样品编号相同：
// 仍必须整次拒绝，不能让确认/作废有机会落错到那份样品上。
func TestOpenMismatchedIDCollidingWithRealSample(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				// 放在 S1 下，却冒写 S2 的编号。
				ID: "S2", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
			"S2": {
				// 真正编号为 S2 的样品。
				ID: "S2", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsID(t, dir, "S1",
		[]string{"S2", "不一致"}, []string{"样品集合中存在重复的样品编号"})
}

// 样品集合为空或缺失照常打开，不要求尚未录入样品的数据已有编号。
func TestOpenEmptySamplesNoIDRequirement(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{Points: openPoints(), Samples: map[string]*Sample{}})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("空样品集合应正常打开: %v", err)
	}
	s.Close()

	writeRawDataFile(t, dir, `{"points":`+func() string {
		b, _ := json.Marshal(openPoints())
		return string(b)
	}()+`}`)
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("样品字段缺失应正常打开: %v", err)
	}
	s2.Close()
}

// 正常录入、确认、作废后保存的数据重新打开：两处编号本来就一致，按点列表、
// 最近有效结果、排序与作废资格全部保持原有行为，落盘文件里两处编号也一致。
func TestOpenNormalFlowIDConsistencyUnchanged(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustSample(t, s, "S2", "P1", at(5, 0), Measurement{Item: openPH, Value: 7}) // 待判定
	mustSample(t, s, "S1", "P1", at(5, 0), Measurement{Item: openPH, Value: 9}) // 同时刻，编号升序
	mustSample(t, s, "S3", "P1", at(8, 0), Measurement{Item: openPH, Value: 9}) // 待确认
	mustSample(t, s, "S4", "P1", at(9, 0), Measurement{Item: openPH, Value: 7}) // 将作废
	if _, err := s.Confirm("S3"); err != nil {
		t.Fatalf("Confirm S3: %v", err)
	}
	if _, err := s.Void("S4", "采样瓶破损"); err != nil {
		t.Fatalf("Void S4: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 落盘文件中每条样品记录的 id 都与其集合键一致（沿用正常保存形式）。
	raw, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var saved diskState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	for key, smp := range saved.Samples {
		if smp == nil || smp.ID != key {
			id := ""
			if smp != nil {
				id = smp.ID
			}
			t.Fatalf("正常保存的记录两处编号必须一致：键 %q vs id %q", key, id)
		}
	}

	// 重新打开一切照旧：样品、所用上限、历史结论、排序、作废资格都不变。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的文件应照常打开: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	gotIDs := []string{list[0].ID, list[1].ID, list[2].ID, list[3].ID}
	wantIDs := []string{"S4", "S3", "S1", "S2"} // 时间晚到早，同时刻编号升序
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("排序被改动: got %v want %v", gotIDs, wantIDs)
		}
	}
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S3" {
		t.Fatalf("最近有效结果应仍为 S3: %+v ok=%v err=%v", latest, ok, err)
	}
	if r := latest.Results[0]; r.Value != 9 || r.Limit != 8 ||
		!r.LimitEffective.Equal(openEff()) || !r.Exceeded || !latest.Exceeded {
		t.Fatalf("所用上限与历史结论必须原样保留: %+v", r)
	}
	if _, err := s2.Confirm("S4"); !errors.Is(err, ErrVoided) {
		t.Fatalf("作废资格不变，S4 再确认应被 ErrVoided 拒绝，got %v", err)
	}
	if _, err := s2.Void("S3", "再次作废"); err != nil {
		t.Fatalf("已确认样品仍可按编号作废: %v", err)
	}
	if _, err := s2.Confirm("S9"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("不存在的编号仍报样品不存在，got %v", err)
	}
}

// 直接用 Open 打开 S1 条目下 id=S2 的文件前后，文件字节必须保持不变：
// 不补编号、不把条目移动或改名、不改状态，便于人工修复后原样重试。
func TestOpenMismatchedIDLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": mismatchedS1(StatusConfirmed),
		},
	})
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("编号不一致必须打开失败")
	}
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("打开失败不得改动原文件")
	}
	// 错误信息里两处编号都在，调用方知道是集合 S1 下的记录把 id 写成了 S2。
	_, err = Open(dir)
	if err == nil {
		t.Fatal("应当失败")
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("got %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("样品集合编号 %q", "S1")) ||
		!strings.Contains(msg, `记录自身编号 "S2"`) {
		t.Fatalf("错误信息应同时指出集合编号 S1 与记录自身编号 S2，实际 %q", msg)
	}
}
