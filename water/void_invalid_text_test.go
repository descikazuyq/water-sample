package water

import (
	"errors"
	"reflect"
	"testing"
)

// 本文件的测试只针对一件事：Void 只接受能作为合法 UTF-8 原样保存的作废原因。
// 原因是之后核对历史的依据：落盘 JSON 会把孤立 0xFF、0xFE、不完整的多字节
// 序列等非法字节静默替换成 U+FFFD，调用当场返回的原因仍带原始字节，保存后的
// 记录却变成“�”，同一份记录保存前后对不上；重新打开后再拿原原因重复作废
// 还会被当成不同原因报冲突。因此原始编号或原因含非法字节时，本次作废必须
// 整体拒绝：返回空样品和可被 errors.Is(err, water.ErrInvalidText) 识别的编码
// 错误，不删除、不替换任何字节，也不改变样品状态、原因与历史依据。即使样品
// 编号不存在，或样品已作废而这次原因与原原因不同，也报编码错误而不是样品
// 不存在或原因冲突；数据存放已关闭时仍先返回 ErrClosed。真正的 U+FFFD
// （“�”）、中文、emoji 与文本内部的 U+0000 都是合法字符。

// assertVoidEncodingError 锁定作废的编码拒绝：必须是 ErrInvalidText，
// 且不能冒充样品不存在、空白字段、作废原因冲突或已作废等其它错误。
func assertVoidEncodingError(t *testing.T, err error, label string) {
	t.Helper()
	assertEncodingError(t, err, label)
	for _, target := range []error{ErrEmptyField, ErrUnknownSample, ErrVoided, ErrVoidReasonConflict} {
		if errors.Is(err, target) {
			t.Fatalf("%s: 编码问题不能被归为 %v: %v", label, target, err)
		}
	}
}

// 待判定、已确认、已作废三种样品同时存在时，非法原因的作废请求必须整体
// 拒绝：待判定继续待判定，已确认继续保有原结论，已作废继续保留原原因与
// 历史依据；按点查看的测量值、状态、逐项所用上限、整份超标标记与最近有效
// 结果都与请求前一致。编号不存在或编号自身含非法字节也报编码错误。失败
// 不落盘（文件中不出现替换字符），之后用合法原因作废仍按原有规则处理。
func TestVoidRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// 另一个只有一份待判定样品的采样点：失败后最近有效结果仍明确“无结果”。
	mustPoint(t, s, "P2", "二号取水口")

	// S0 较早、达标、已确认。
	mustSample(t, s, "S0", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	if _, err := s.Confirm("S0"); err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	// SP 待判定。
	mustSample(t, s, "SP", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	// SC 较晚、一项超标一项等于上限、整份超标、已确认，是当前最近有效结果。
	mustSample(t, s, "SC", "P1", at(11, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	scConfirmed, err := s.Confirm("SC")
	if err != nil {
		t.Fatalf("Confirm SC: %v", err)
	}
	// SV 已作废，原因与历史依据齐全。
	mustSample(t, s, "SV", "P1", at(12, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("SV"); err != nil {
		t.Fatalf("Confirm SV: %v", err)
	}
	svVoided, err := s.Void("SV", "采样瓶破损")
	if err != nil {
		t.Fatalf("Void SV: %v", err)
	}
	// P2 唯一一份待判定样品。
	mustSample(t, s, "S2", "P2", at(12, 0), Measurement{Item: "pH", Value: 7})

	// 请求前的完整台账与最近有效结果快照，失败后必须逐字段一致。
	beforeP1, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint P1: %v", err)
	}
	beforeLatest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || beforeLatest.ID != "SC" {
		t.Fatalf("请求前最近有效结果应为 SC: %+v ok=%v err=%v", beforeLatest, ok, err)
	}
	beforeP2, err := s.ListByPoint("P2")
	if err != nil {
		t.Fatalf("ListByPoint P2: %v", err)
	}

	badReasons := []string{
		"原因\xff",          // 孤立 0xFF
		"原因\xfe",          // 孤立 0xFE
		"原\xe4\xb8因",      // 合法中文夹不完整的三字节序列
		"\xe4\xb8原因",      // 开头不完整序列
		"原因\xc0\xaf",      // 非法多字节序列
		"  原因\xff  ",      // 首尾空白不能掩盖非法字节
		"\xff",            // 整段只有非法字节
		"原因" + "\xff\xfe", // 连续两个孤立非法字节
	}

	reject := func(label, id, reason string) {
		t.Helper()
		got, err := s.Void(id, reason)
		assertVoidEncodingError(t, err, label)
		checkEmptySample(t, got, label)
	}
	for _, r := range badReasons {
		// 待判定样品：不能被作废，也不能留下原因或失败标记。
		reject("待判定样品+非法原因 "+r, "SP", r)
		// 已确认样品：不能被作废，原结论与逐项依据不动。
		reject("已确认样品+非法原因 "+r, "SC", r)
		// 已作废样品且非法原因与原原因不同：必须报编码错误，不能报原因冲突。
		reject("已作废样品+不同的非法原因 "+r, "SV", r)
		// 编号不存在：仍先报编码错误，不能报样品不存在。
		reject("不存在编号+非法原因 "+r, "S9", r)
	}
	// 编号自身含非法字节时同样报编码错误：无论原因合法非法、样品存在与否。
	reject("编号含 0xFF、原因合法", "SV"+"\xff", "合法原因")
	reject("编号含 0xFE、样品本不存在", "S9"+"\xfe", "合法原因")
	reject("编号含不完整序列且首尾空白", "  SV"+"\xe4\xb8  ", "x")
	reject("编号与原因都含非法字节", "SC"+"\xff", "原因"+"\xfe")

	// 对照：全合法但样品不存在，仍是样品不存在；空白原因仍是空白字段。
	if _, err := s.Void("S9", "合法原因"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("对照不存在样品应报 ErrUnknownSample，got %v", err)
	}
	if _, err := s.Void("SP", "   "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("空白原因仍应按现有规则拒绝，got %v", err)
	}

	// 全部失败之后：按点台账与请求前逐字段一致。
	afterP1, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint P1 after: %v", err)
	}
	if !reflect.DeepEqual(afterP1, beforeP1) {
		t.Fatalf("非法作废请求改变了按点台账:\n got %+v\nwant %+v", afterP1, beforeP1)
	}
	afterP2, err := s.ListByPoint("P2")
	if err != nil {
		t.Fatalf("ListByPoint P2 after: %v", err)
	}
	if !reflect.DeepEqual(afterP2, beforeP2) {
		t.Fatalf("非法作废请求改变了 P2 台账: %+v", afterP2)
	}
	if afterP2[0].Status != StatusPending || afterP2[0].VoidReason != "" || len(afterP2[0].Results) != 0 {
		t.Fatalf("P2 待判定样品应原样: %+v", afterP2[0])
	}
	// 最近有效结果仍是已确认的 SC，结论与逐项依据完整。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果变化: %+v ok=%v err=%v", latest, ok, err)
	}
	if !reflect.DeepEqual(latest, beforeLatest) || !reflect.DeepEqual(latest, scConfirmed) {
		t.Fatalf("最近有效结果被失败请求改动:\n got %+v\nwant %+v", latest, scConfirmed)
	}
	// P2 仍明确无结果。
	if none, ok, err := s.LatestResult("P2"); err != nil || ok || none.ID != "" {
		t.Fatalf("P2 仍应无最近有效结果: %+v ok=%v err=%v", none, ok, err)
	}
	// SC 的已确认结论：pH 9 > 8 超标、COD 30 == 30 达标，整份超标，
	// 采样于 9/11。失败请求前后都必须逐字段保持。
	checkSCConfirmed := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "SC" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
			t.Fatalf("%s: SC 基本字段被改动: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(at(11, 0)) || !smp.Exceeded || len(smp.Results) != 2 {
			t.Fatalf("%s: SC 时间、超标标记或依据被改动: %+v", label, smp)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if ph := rs["pH"]; ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
			t.Fatalf("%s: SC 的 pH 依据被改动: %+v", label, ph)
		}
		if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
			t.Fatalf("%s: SC 的 COD 依据被改动: %+v", label, cod)
		}
	}
	checkSCConfirmed(scConfirmed, "失败前 SC 快照")
	checkSCConfirmed(latest, "失败后最近有效结果 SC")
	// 已作废样品保留原原因与历史依据。
	if !reflect.DeepEqual(svVoided, findSample(afterP1, "SV")) {
		t.Fatalf("已作废记录被失败请求改动:\n got %+v\nwant %+v", findSample(afterP1, "SV"), svVoided)
	}
	// 待判定样品仍可正常确认，失败请求没有占用或破坏它。
	spConfirmed, err := s.Confirm("SP")
	if err != nil {
		t.Fatalf("失败请求后待判定样品应仍能确认: %v", err)
	}
	if spConfirmed.Status != StatusConfirmed || spConfirmed.Exceeded || len(spConfirmed.Results) != 2 {
		t.Fatalf("SP 确认结果异常: %+v", spConfirmed)
	}
	// 任何被拒绝的原因都不能被替换成 U+FFFD 落盘。
	assertNoReplacementBytes(t, dir, "全部非法作废请求被拒绝后")

	// 之后用合法原因作废同一批样品，按原有规则处理，不受此前失败请求影响。
	scVoid, err := s.Void(" SC ", "  复测确认超标  ")
	if err != nil {
		t.Fatalf("合法原因作废 SC: %v", err)
	}
	if scVoid.Status != StatusVoided || scVoid.VoidReason != "复测确认超标" || !scVoid.Exceeded || len(scVoid.Results) != 2 {
		t.Fatalf("SC 合法作废应保留原因与历史依据: %+v", scVoid)
	}
	// SC 作废后最近有效结果退到次晚的 SP。
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "SP" {
		t.Fatalf("SC 作废后最近有效结果应退到 SP: %+v ok=%v err=%v", latest2, ok, err)
	}
	// 处理后相同的原因（带首尾空白）仍幂等返回原记录。
	if same, err := s.Void("SC", " 复测确认超标 "); err != nil || !reflect.DeepEqual(same, scVoid) {
		t.Fatalf("相同合法原因应幂等返回原记录: %+v err=%v", same, err)
	}
	// 不同的合法原因仍报已有的原因冲突。
	if _, err := s.Void("SC", "另一个合法原因"); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("不同合法原因应报原因冲突，got %v", err)
	}
	// 此时再提交此前被拒的原始非法字节原因：仍是编码错误，不能落成冲突。
	if _, err := s.Void("SC", badReasons[0]); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("作废后再提非法原因仍应报编码错误，got %v", err)
	}
	// SP（现已确认）也能用合法原因作废，保留它达标依据。
	spVoid, err := s.Void("SP", "信息补录错误")
	if err != nil {
		t.Fatalf("合法原因作废 SP: %v", err)
	}
	if spVoid.Status != StatusVoided || spVoid.VoidReason != "信息补录错误" || spVoid.Exceeded || len(spVoid.Results) != 2 {
		t.Fatalf("SP 合法作废异常: %+v", spVoid)
	}
	// 已作废样品不能再次确认，原有行为不受影响。
	if _, err := s.Confirm("SC"); !errors.Is(err, ErrVoided) {
		t.Fatalf("作废后再次确认应报 ErrVoided，got %v", err)
	}
	// P2 的待判定样品此前失败后仍可合法作废，且不会凭空得到判定依据。
	s2Void, err := s.Void("S2", "登记有误")
	if err != nil {
		t.Fatalf("P2 样品合法作废: %v", err)
	}
	if s2Void.Status != StatusVoided || len(s2Void.Results) != 0 || s2Void.Exceeded {
		t.Fatalf("待判定样品作废不应凭空生成依据: %+v", s2Void)
	}

	// 关闭后重新打开：三份作废记录的原因与历史依据原样保留，
	// 在新进程里重放非法原因仍是编码错误，合法原因仍按原规则幂等/冲突。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoReplacementBytes(t, dir, "关闭前")
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 4 {
		t.Fatalf("重开后台账异常: %+v err=%v", list, err)
	}
	reopenedSC := findSample(list, "SC")
	if reopenedSC.Status != StatusVoided || reopenedSC.VoidReason != "复测确认超标" ||
		!reopenedSC.Exceeded || len(reopenedSC.Results) != 2 {
		t.Fatalf("重开后 SC 作废记录异常: %+v", reopenedSC)
	}
	if !reflect.DeepEqual(reopenedSC, scVoid) {
		t.Fatalf("重开后 SC 与作废返回记录不一致:\n got %+v\nwant %+v", reopenedSC, scVoid)
	}
	reopenedSV := findSample(list, "SV")
	if reopenedSV.VoidReason != "采样瓶破损" || !reflect.DeepEqual(reopenedSV, svVoided) {
		t.Fatalf("重开后 SV 原原因与历史依据被改动:\n got %+v\nwant %+v", reopenedSV, svVoided)
	}
	// 最近有效结果只剩最早的 S0。
	latest3, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest3.ID != "S0" {
		t.Fatalf("重开后最近有效结果应为 S0: %+v ok=%v err=%v", latest3, ok, err)
	}
	// 重放原非法字节原因：保存的原因里没有“�”，因此绝不能被判成原因冲突。
	for _, r := range badReasons {
		if _, err := s2.Void("SC", r); !errors.Is(err, ErrInvalidText) {
			t.Fatalf("重开后非法原因应报编码错误（原因 %q）: %v", r, err)
		}
		if _, err := s2.Void("SV", r); !errors.Is(err, ErrInvalidText) {
			t.Fatalf("重开后对 SV 非法原因应报编码错误（原因 %q）: %v", r, err)
		}
	}
	// 合法原因的幂等与冲突规则在重开后保持不变。
	if again, err := s2.Void(" SC ", " 复测确认超标 "); err != nil || !reflect.DeepEqual(again, scVoid) {
		t.Fatalf("重开后相同合法原因应幂等返回: %+v err=%v", again, err)
	}
	if _, err := s2.Void("SV", " 采样瓶破损 "); err != nil {
		t.Fatalf("重开后 SV 相同原因应幂等返回: %v", err)
	}
	if _, err := s2.Void("SV", "另一个合法原因"); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("重开后不同合法原因应报冲突，got %v", err)
	}
	for _, id := range []string{"SP", "SC", "SV"} {
		if _, err := s2.Confirm(id); !errors.Is(err, ErrVoided) {
			t.Fatalf("重开后 %s 再次确认应仍被拒绝，got %v", id, err)
		}
	}
}

func findSample(list []Sample, id string) Sample {
	for _, smp := range list {
		if smp.ID == id {
			return smp
		}
	}
	return Sample{}
}

// 真正的 U+FFFD（“�”）、中文、emoji、文本内部的 U+0000 都是合法内容：
// 只去首尾空白，不拒绝也不改写。成功作废后返回记录与按点查看到的原因一致，
// 关闭后重新打开也保留同样文本；处理后相同的原因仍返回原作废记录，不同的
// 合法原因仍报原因冲突。
func TestVoidAcceptsValidUnicodeText(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")

	reasons := []struct {
		id    string
		given string // 提交的原始原因
		saved string // 保存时只去首尾空白后的原因
	}{
		{"S1", "  采样瓶破损  ", "采样瓶破损"}, // 中文 + 首尾空白
		{"S2", "瓶子碎了🫧", "瓶子碎了🫧"},     // emoji（四字节序列）
		{"S3", "中\x00间", "中\x00间"},   // 文本内部的 U+0000
		{"S4", "  浊�度  ", "浊�度"},     // 真正编码为 U+FFFD 的“�”
		{"S5", "  �  ", "�"},         // 原因只有真正的 U+FFFD
	}
	for i, c := range reasons {
		mustSample(t, s, c.id, "P1", at(10+i, 0), Measurement{Item: "pH", Value: 7})
	}
	// 作废前该点没有最近有效结果，合法作废不应凭空造出结论。
	if _, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废前不应有最近有效结果")
	}

	voided := map[string]Sample{}
	for _, c := range reasons {
		got, err := s.Void(c.id, c.given)
		if err != nil {
			t.Fatalf("合法原因 %q 不应被拒绝: %v", c.given, err)
		}
		if got.Status != StatusVoided || got.VoidReason != c.saved {
			t.Fatalf("原因只应去掉首尾空白，got %+v", got)
		}
		voided[c.id] = got
	}

	// 返回记录与按点查看到的原因一致。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != len(reasons) {
		t.Fatalf("按点查看异常: %+v err=%v", list, err)
	}
	for _, c := range reasons {
		got := findSample(list, c.id)
		if !reflect.DeepEqual(got, voided[c.id]) {
			t.Fatalf("按点查看与作废返回记录不一致（%s）:\n got %+v\nwant %+v",
				c.id, got, voided[c.id])
		}
	}
	if _, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废记录不应成为最近有效结果")
	}

	// 关闭后重新打开，同样的文本原样保留；逐项比较原因，确保 U+0000 与
	// 真正的 U+FFFD 都没有被改写或吞掉。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != len(reasons) {
		t.Fatalf("重开后按点查看异常: %+v err=%v", reopened, err)
	}
	for _, c := range reasons {
		got := findSample(reopened, c.id)
		if got.VoidReason != c.saved || got.Status != StatusVoided {
			t.Fatalf("重开后原因 %q 未原样保存，got %+v", c.saved, got)
		}
		// 处理后相同的原因（允许首尾空白）仍返回原作废记录。
		again, err := s2.Void(" "+c.id+" ", "  "+c.saved+"  ")
		if err != nil {
			t.Fatalf("重开后相同原因应幂等返回（%s）: %v", c.id, err)
		}
		if again.VoidReason != c.saved || again.Status != StatusVoided {
			t.Fatalf("幂等返回记录异常: %+v", again)
		}
		// 不同的合法原因仍报已有的原因冲突。
		if _, err := s2.Void(c.id, c.saved+"-另一个合法原因"); !errors.Is(err, ErrVoidReasonConflict) {
			t.Fatalf("重开后不同合法原因应报冲突（%s）: %v", c.id, err)
		}
		// 含非法字节的不同原因仍是编码错误，不能被归为原因冲突。
		if _, err := s2.Void(c.id, c.saved+"\xff"); !errors.Is(err, ErrInvalidText) {
			t.Fatalf("重开后非法原因应报编码错误（%s）: %v", c.id, err)
		}
		// 空白原因仍按现有规则在冲突判断之前拒绝。
		if _, err := s2.Void(c.id, " \t\n "); !errors.Is(err, ErrEmptyField) {
			t.Fatalf("空白原因应报 ErrEmptyField（%s）: %v", c.id, err)
		}
	}
}

// 已关闭的数据存放仍按原有规则拒绝一切操作：即使编号或原因含非法 UTF-8，
// 也返回 ErrClosed 而不是 ErrInvalidText，编码检查不改变关闭语义。
func TestVoidInvalidUTF8OnClosedStore(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustSample(t, s, "S1", "P1", at(1, 0), Measurement{Item: "pH", Value: 7})
	if _, err := s.Void("S1", "采样瓶破损"); err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Void("S1", "原因"+"\xff"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭+非法原因应返回 ErrClosed，got %v", err)
	}
	if _, err := s.Void("S9"+"\xfe", "原因"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭+非法编号应返回 ErrClosed，got %v", err)
	}
	if _, err := s.Void("S1", "另一个合法原因"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭+合法原因也应返回 ErrClosed，got %v", err)
	}
}
