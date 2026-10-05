package water

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件的测试只针对一件事：作废原因只接受能作为合法 UTF-8 原样保存的文本。
// 原始原因含孤立 0xFF、0xFE 字节或不完整的多字节序列时，本次作废必须整体
// 拒绝，返回空样品和可被 errors.Is(err, water.ErrInvalidText) 识别的编码错误：
// 不删除或替换字节后继续作废，不改变样品状态、原原因与历史依据，也不改变
// 按点查看与最近有效结果。即使样品编号不存在，或样品已作废且这次原因与原
// 原因不同，也必须报告编码问题，而不是样品不存在或原因冲突；数据存放已
// 关闭时仍返回原有的关闭错误。真正的 U+FFFD（“�”）、中文、表情和内部
// U+0000 仍是合法原因，文本处理只去首尾空白。

// assertVoidEncodingError 断言作废被拒绝为文本编码错误，且没有冒充成
// 样品不存在、原因冲突、空字段或已作废等其它错误。
func assertVoidEncodingError(t *testing.T, got Sample, err error, label string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidText) {
		t.Fatalf("%s: 应返回 ErrInvalidText，实际为 %v", label, err)
	}
	for _, target := range []error{
		ErrEmptyField, ErrUnknownSample, ErrVoided, ErrVoidReasonConflict,
	} {
		if errors.Is(err, target) {
			t.Fatalf("%s: 编码问题不能被归为 %v: %v", label, target, err)
		}
	}
	if got.ID != "" || got.PointID != "" || got.Status != "" ||
		got.VoidReason != "" || len(got.Measurements) != 0 || len(got.Results) != 0 {
		t.Fatalf("%s: 失败必须返回空样品，实际为 %+v", label, got)
	}
}

func assertNoVoidReplacement(t *testing.T, dir, label string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("%s: 读取落盘文件: %v", label, err)
	}
	if bytes.Contains(data, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("%s: 被拒绝的非法原因被替换成 U+FFFD 落盘", label)
	}
}

// setupConfirmedS1 准备采样点 P1（pH、COD 上限）和一份已确认、整份超标的
// 双项目样品 S1：pH 9 > 8，COD 30 == 30。
func setupConfirmedS1(t *testing.T, s *Store) Sample {
	t.Helper()
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	conf := mustConfirm(t, s, "S1")
	if conf.Status != StatusConfirmed || !conf.Exceeded {
		t.Fatalf("setup: S1 应为已确认且整份超标: %+v", conf)
	}
	return conf
}

// 各种非法 UTF-8 原因：孤立字节、不完整/非法多字节序列、合法中文后接非法
// 字节、首尾带空白的非法文本（空白只做去除，不能顺带把非法字节洗成合法）。
var badVoidReasons = []string{
	"原因\xff",       // 孤立 0xFF
	"原因\xfe",       // 孤立 0xFE
	"原因\xe4\xb8",   // 不完整的三字节序列
	"原因\xc0\xaf",   // 非法多字节序列
	"采样瓶破损" + "\xff", // 合法中文后接孤立 0xFF
	"  原因\xff  ", // 首尾空白内夹非法字节，去空白也救不回来
	"\xe4\xb8原因", // 开头不完整序列
}

// 待判定样品被非法原因作废：整次拒绝，样品继续待判定，测量值不变，
// 按点查看与最近有效结果都与请求前一致；随后合法原因作废不受影响。
func TestVoidPendingRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 7})

	for i, reason := range badVoidReasons {
		got, err := s.Void("S1", reason)
		assertVoidEncodingError(t, got, err, "待判定样品+非法原因#"+string(rune('0'+i)))
	}

	// 待判定资格原样保留：测量值、状态、无判定依据
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看数量变化: %+v err=%v", list, err)
	}
	kept := list[0]
	if kept.ID != "S1" || kept.Status != StatusPending || kept.VoidReason != "" ||
		kept.Exceeded || len(kept.Results) != 0 {
		t.Fatalf("待判定样品应原样保留: %+v", kept)
	}
	if len(kept.Measurements) != 1 || kept.Measurements[0].Item != "pH" || kept.Measurements[0].Value != 7 {
		t.Fatalf("测量值被改动: %+v", kept.Measurements)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok || latest.ID != "" {
		t.Fatalf("最近有效结果不应变化: %+v ok=%v err=%v", latest, ok, err)
	}
	assertNoVoidReplacement(t, dir, "全部拒绝后")

	// 之后用合法原因作废同一份样品，按原有规则成功处理
	v, err := s.Void("S1", "  录入信息有误  ")
	if err != nil {
		t.Fatalf("先前失败不应影响合法作废: %v", err)
	}
	if v.Status != StatusVoided || v.VoidReason != "录入信息有误" {
		t.Fatalf("合法原因应正常作废: %+v", v)
	}
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("作废后不能再次确认，got %v", err)
	}
}

// 已确认样品被非法原因作废：整次拒绝，样品继续保有原结论——测量值、
// 逐项所用上限与生效时间、整份超标标记全部不变，最近有效结果仍指向它。
func TestVoidConfirmedRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	setupConfirmedS1(t, s)

	for _, reason := range badVoidReasons {
		got, err := s.Void("S1", reason)
		assertVoidEncodingError(t, got, err, "已确认样品+非法原因 "+reason)
	}

	// 原已确认结论完整保留（复用 invalid_text_test.go 的核对逻辑）
	checkTwoItemSaved(t, mustConfirm(t, s, "S1"), "非法作废后重复确认")
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看数量变化: %+v err=%v", list, err)
	}
	checkTwoItemSaved(t, list[0], "非法作废后按点查看")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果变化: %+v ok=%v err=%v", latest, ok, err)
	}
	checkTwoItemSaved(t, latest, "非法作废后最近有效结果")
	assertNoVoidReplacement(t, dir, "全部拒绝后")

	// 之后合法原因作废照常成功：保留判定依据、退出最近有效结果
	v, err := s.Void("S1", "复测确认样品污染")
	if err != nil {
		t.Fatalf("合法原因作废: %v", err)
	}
	if v.Status != StatusVoided || v.VoidReason != "复测确认样品污染" ||
		!v.Exceeded || len(v.Results) != 2 {
		t.Fatalf("合法作废应保留原因与历史依据: %+v", v)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok || latest.ID != "" {
		t.Fatalf("成功作废后最近有效结果应清空: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 样品编号不存在时提交非法原因：必须报告编码问题，而不是样品不存在。
func TestVoidUnknownSampleInvalidReasonReportsEncoding(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	for _, reason := range badVoidReasons {
		got, err := s.Void("S9", reason)
		assertVoidEncodingError(t, got, err, "不存在样品+非法原因")
	}
	// 对照：编号不存在但原因合法，仍是原有的样品不存在错误
	if got, err := s.Void("S9", "原因"); !errors.Is(err, ErrUnknownSample) || got.ID != "" {
		t.Fatalf("合法原因+不存在样品应报 ErrUnknownSample，got %+v err=%v", got, err)
	}
	// 编码问题优先于空字段：原因既非法又（去空白后）为空
	got, err := s.Void("S9", "  \xff  ")
	assertVoidEncodingError(t, got, err, "不存在样品+非法空白原因")
}

// 已作废样品再次提交与原原因不同的非法原因：必须报告编码问题，而不是
// 原因冲突；原原因与历史依据完整保留。处理后相同的合法原因仍幂等返回。
func TestVoidedInvalidReasonReportsEncodingNotConflict(t *testing.T) {
	s, dir := open(t)
	setupConfirmedS1(t, s)
	const orig = "采样瓶破损"
	voided, err := s.Void("S1", "  "+orig+"  ")
	if err != nil {
		t.Fatalf("首次作废: %v", err)
	}
	if voided.Status != StatusVoided || voided.VoidReason != orig {
		t.Fatalf("首次作废结果异常: %+v", voided)
	}

	for _, reason := range badVoidReasons {
		got, err := s.Void("S1", reason)
		assertVoidEncodingError(t, got, err, "已作废样品+非法原因 "+reason)
	}

	// 原原因与历史依据原样保留
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看数量变化: %+v err=%v", list, err)
	}
	kept := list[0]
	if kept.Status != StatusVoided || kept.VoidReason != orig ||
		!kept.Exceeded || len(kept.Results) != 2 {
		t.Fatalf("原作废原因与历史依据被改动: %+v", kept)
	}
	// 最近有效结果仍为空（失败请求不能让它恢复资格）
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok || latest.ID != "" {
		t.Fatalf("最近有效结果不应恢复: %+v ok=%v err=%v", latest, ok, err)
	}
	// 已作废样品再次确认仍被拒绝
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("已作废样品仍不能确认，got %v", err)
	}
	assertNoVoidReplacement(t, dir, "非法重报后")

	// 相同合法原因仍返回原作废记录；不同合法原因仍报原因冲突
	same, err := s.Void("S1", "  "+orig+"  ")
	if err != nil || same.Status != StatusVoided || same.VoidReason != orig {
		t.Fatalf("相同合法原因应幂等返回原记录: %+v err=%v", same, err)
	}
	if _, err := s.Void("S1", "另一个合法原因"); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("不同合法原因应报冲突，got %v", err)
	}
}

// 中文、表情、文本内部 U+0000 和真正的 U+FFFD（“�”）都是合法原因：
// 只去首尾空白，不拒绝也不改写。成功作废后返回记录与按点查看一致，
// 关闭后重新打开仍保留同样文本；相同原因幂等，不同合法原因仍冲突。
func TestVoidAcceptsValidUnicodeReason(t *testing.T) {
	s, dir := open(t)
	setupConfirmedS1(t, s)

	reasons := []string{
		"采样瓶破损",      // 中文
		"样品污染🫧复测",   // 含表情（四字节序列）
		"原\x00因",      // 文本内部 U+0000
		"浊�度异常",     // 真正编码为 U+FFFD 的“�”
	}
	// 先成功作废 S1，再验证重开后原因逐字节保留以及幂等/冲突语义。
	reason := "  " + reasons[1] + "  "
	v, err := s.Void("S1", reason)
	if err != nil {
		t.Fatalf("合法 Unicode 原因不应被拒绝: %v", err)
	}
	want := strings.TrimSpace(reason)
	if v.Status != StatusVoided || v.VoidReason != want {
		t.Fatalf("原因只应去掉首尾空白，实际为 %+v", v)
	}

	// 返回记录与按点查看到的原因一致
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].VoidReason != want || list[0].VoidReason != v.VoidReason {
		t.Fatalf("返回记录与按点查看的原因不一致: %+v err=%v", list, err)
	}

	// 关闭后重新打开保留同样文本
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 1 {
		t.Fatalf("重开后按点查看异常: %+v err=%v", reopened, err)
	}
	if reopened[0].Status != StatusVoided || reopened[0].VoidReason != want {
		t.Fatalf("重开后原因被改写: %+v", reopened[0])
	}
	// 重开后相同原因仍返回原作废记录，不同合法原因仍报冲突
	again, err := s2.Void("S1", want)
	if err != nil || again.Status != StatusVoided || again.VoidReason != want {
		t.Fatalf("重开后相同原因应返回原记录: %+v err=%v", again, err)
	}
	if _, err := s2.Void("S1", reasons[0]); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("重开后不同合法原因应报冲突，got %v", err)
	}

	// 其余合法原因各自都能成功作废另一份样品（含 U+FFFD 与内部 U+0000），
	// 并在重开后逐字节保留。
	for i, r := range []string{reasons[0], reasons[2], reasons[3]} {
		id := "S" + string(rune('2'+i))
		mustSample(t, s2, id, "P1", at(20+i, 0), Measurement{Item: "pH", Value: 7})
		vv, err := s2.Void(id, r)
		if err != nil {
			t.Fatalf("合法原因 %q 不应被拒绝: %v", r, err)
		}
		if vv.VoidReason != r {
			t.Fatalf("原因被改写: got %q want %q", vv.VoidReason, r)
		}
	}
}

// 空白原因仍按现有规则以 ErrEmptyField 拒绝；数据存放关闭后即使原因非法，
// 也仍返回原有的关闭错误。
func TestVoidBlankReasonAndClosedStore(t *testing.T) {
	s, dir := open(t)
	setupConfirmedS1(t, s)

	// 仅含空白的合法文本：空字段错误，不是编码错误
	for _, blank := range []string{"", "   ", "\t\n "} {
		got, err := s.Void("S1", blank)
		if !errors.Is(err, ErrEmptyField) || errors.Is(err, ErrInvalidText) {
			t.Fatalf("空白原因应报 ErrEmptyField，got %+v err=%v", got, err)
		}
		if got.ID != "" || got.Status != "" {
			t.Fatalf("失败必须返回空样品，got %+v", got)
		}
	}

	// 关闭后：关闭错误优先于一切，非法原因也不改变这一点
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, reason := range append([]string{"原因"}, badVoidReasons...) {
		if _, err := s.Void("S1", reason); !errors.Is(err, ErrClosed) {
			t.Fatalf("关闭后应返回 ErrClosed（原因 %q），got %v", reason, err)
		}
	}
	// 重新打开后样品仍是关闭前的已确认状态，失败请求什么都没留下
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].Status != StatusConfirmed || list[0].VoidReason != "" {
		t.Fatalf("重开后状态异常: %+v err=%v", list, err)
	}
}
