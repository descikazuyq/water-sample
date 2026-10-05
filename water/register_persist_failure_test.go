package water

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 本文件的测试只保护 RegisterPoint 的一条既有行为：登记请求本身合法、
// 但本地数据无法保存时，本次登记整体失败，返回保存错误和编号、名称均为空的
// 采样点——“登记被拒绝就不留下记录”。失败的编号既不被占用，也不会成为
// 能登记限值的已知采样点；保存失败必须与内容校验失败（空字段、重复编号等）
// 明确区分。保障分两种起始状态：第一次使用、尚无成功登记的空数据存放，
// 以及数据中已经有成功登记的采样点、限值与样品时再登记失败。

// assertRegisterSaveFailure 断言一次合法登记走到了保存阶段并因保存失败被拒绝：
// 必须返回非空的保存错误，且不能冒充任何一种内容校验错误，返回值必须是
// 编号、名称均为空的采样点，编号在内存登记中也不得留下。
func assertRegisterSaveFailure(t *testing.T, s *Store, label, id, name string) {
	t.Helper()
	got, err := s.RegisterPoint(id, name)
	if err == nil {
		t.Fatalf("%s: 本地无法保存时 RegisterPoint 必须失败", label)
	}
	for _, target := range []error{
		ErrEmptyField, ErrDuplicatePoint, ErrInvalidText, ErrUnknownPoint, ErrClosed,
	} {
		if errors.Is(err, target) {
			t.Fatalf("%s: 保存失败不能被说成 %v: %v", label, target, err)
		}
	}
	if got.ID != "" || got.Name != "" {
		t.Fatalf("%s: 保存失败必须返回编号、名称均为空的采样点，实际为 %+v", label, got)
	}
	if _, ok := s.points[id]; ok {
		t.Fatalf("%s: 保存失败后编号 %q 不得留在内存登记中", label, id)
	}
}

// 第一次使用空数据存放时登记失败：此前没有任何成功登记，失败编号不得被占用，
// 也不能成为能登记限值的已知采样点（随后给它登记限值仍报采样点未登记）。
// 保存条件恢复后，调用方先成功登记另一个编号，关闭再重新打开同一数据存放，
// 之前失败的编号仍应按未登记处理，不能被别的成功操作顺带落盘。此时给失败编号
// 加上首尾空白、换一个与失败请求不同的合法名称，应作为首次登记成功接受，
// 返回的编号、名称都是去掉首尾空白后的内容，名称只来自这次成功请求；
// 再次登记同一编号恢复现有的重复编号拒绝，成功名称不被第二次名称替换。
func TestRegisterPointPersistFailureOnEmptyStore(t *testing.T) {
	s, dir := open(t)

	// 让落盘失败：把临时文件路径占成目录，原子写的 WriteFile 必然失败。
	// 此时数据文件尚不存在，正是“第一次使用空数据存放”的情形。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	failedID, failedName := "P2", "  临时名称  "
	assertRegisterSaveFailure(t, s, "空数据存放首次登记失败", failedID, failedName)

	// 失败编号没有成为已知采样点：给它登记限值仍是现有的“采样点未登记”，
	// 而不是重复编号或保存失败。
	if _, err := s.SetLimit("P2", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("失败编号不得用于登记限值，got %v", err)
	}

	// 保存条件未恢复期间，用同一编号同一名称重试仍应因保存失败被拒绝，
	// 不能因为编号“似乎登记过”而走重复编号分支。
	assertRegisterSaveFailure(t, s, "保存未恢复时重试", "P2", "临时名称")

	// 恢复正常保存条件。
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 调用方先成功登记另一个编号：这次成功保存不能把失败的 P2 顺带写进去。
	other, err := s.RegisterPoint("P3", "三号点")
	if err != nil {
		t.Fatalf("恢复后登记其它编号应成功: %v", err)
	}
	if other.ID != "P3" || other.Name != "三号点" {
		t.Fatalf("成功登记返回内容异常: %+v", other)
	}

	// 关闭后重新打开同一数据存放：P3 在，P2 仍按未登记处理。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.points["P3"]; got.ID != "P3" || got.Name != "三号点" {
		t.Fatalf("重开后成功登记的采样点异常: %+v", got)
	}
	if _, ok := s2.points["P2"]; ok {
		t.Fatal("重开后失败编号不得随其它成功操作一并保存")
	}
	if _, err := s2.SetLimit("P2", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("重开后失败编号仍应未登记，got %v", err)
	}

	// 给失败编号加上首尾空白，填写一个与失败请求不同的合法名称：
	// 去空白后判断的仍是同一个编号 P2，应作为首次登记成功接受，
	// 返回的编号、名称均为去掉首尾空白后的内容，名称只来自这次成功请求。
	p, err := s2.RegisterPoint("  P2  ", "  正式名称  ")
	if err != nil {
		t.Fatalf("恢复后失败编号应能首次登记成功: %v", err)
	}
	if p.ID != "P2" || p.Name != "正式名称" {
		t.Fatalf("成功登记应返回去空白后的编号与本次名称，got %+v", p)
	}

	// 现在它是能登记限值的已知采样点。
	if _, err := s2.SetLimit(" P2 ", "pH", 8.0, at(1, 0)); err != nil {
		t.Fatalf("成功登记后应能登记限值: %v", err)
	}

	// 再次登记同一编号恢复现有的重复编号拒绝，成功名称不被第二次名称替换。
	dup, err := s2.RegisterPoint("P2", "又一个名称")
	if !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("成功登记后重复编号应被拒绝，got %v", err)
	}
	if dup.ID != "" || dup.Name != "" {
		t.Fatalf("被拒绝的登记必须返回空采样点，got %+v", dup)
	}
	if got := s2.points["P2"]; got.ID != "P2" || got.Name != "正式名称" {
		t.Fatalf("重复登记不得替换成功名称: %+v", got)
	}
}

// 数据中已经有成功登记的采样点（含限值与已确认样品）时再登记失败：
// 必须返回保存错误和空采样点；原采样点的编号、名称不变，已关联的限值和
// 样品仍归属于它，按点查询看到的样品与已保存判定依据（测量值、状态、
// 逐项所用上限及生效时间、整份结论）保持原样，原有采样点可继续使用。
// 同样无法保存的条件下，提交已有编号仍返回现有的重复编号错误，不能被说成
// 保存失败。恢复后先成功登记别的编号并重开数据存放，失败编号仍未登记；
// 换一个合法名称首次登记成功后，再次登记该编号恢复重复编号拒绝。
func TestRegisterPointPersistFailureWithExistingPoint(t *testing.T) {
	s, dir := open(t)

	// 一个此前成功登记、已有关联限值和已确认样品的采样点。
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "前置确认")

	// 只让新采样点的保存失败：采样点、限值和样品此前均已成功落盘。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 合法的新编号登记走到保存阶段后失败：保存错误 + 空采样点。
	assertRegisterSaveFailure(t, s, "已有数据时新编号登记失败", "P2", "二号取水口")

	// 失败编号没有被占用，也不能登记限值。
	if _, ok := s.points["P2"]; ok {
		t.Fatal("失败登记后不得留下 P2")
	}
	if _, err := s.SetLimit("P2", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("失败编号不得成为已知采样点，got %v", err)
	}

	// 原采样点编号、名称不变。
	if got := s.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("失败登记改动了原有采样点: %+v", got)
	}
	// 已关联到原采样点的限值仍归属于它，没有丢失或被改挂到失败编号。
	versions := s.limits[limitGroup{pointID: "P1", item: "pH"}]
	if len(versions) != 1 || versions[0].Value != 8.0 || !versions[0].Effective.Equal(at(1, 0)) {
		t.Fatalf("原采样点的限值被改动: %+v", versions)
	}
	if _, ok := s.limits[limitGroup{pointID: "P2", item: "pH"}]; ok {
		t.Fatal("限值不得被挂到失败登记的编号上")
	}
	// 按点查询看到的样品及已保存判定依据保持原样。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("失败登记后按点查询变化: %+v err=%v", list, err)
	}
	checkPHExceededSaved(t, list[0], "失败登记后按点查看")
	// 原样品的测量值、状态、所用上限不受影响：重复确认仍返回原判定。
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "失败登记后重复确认")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("原采样点最近有效结果查询失败: ok=%v err=%v", ok, err)
	}
	checkPHExceededSaved(t, latest, "失败登记后最近有效结果")
	// 失败编号下查不到任何样品。
	if list2, err := s.ListByPoint("P2"); err != nil || len(list2) != 0 {
		t.Fatalf("失败编号下不应有样品，got %+v err=%v", list2, err)
	}

	// 保存失败与内容校验失败明确区分：同样无法保存时，提交已有编号仍在保存
	// 之前的校验阶段返回现有的重复编号错误，不能被说成保存失败；
	// 去首尾空白的规则继续适用，" P1 " 判断的仍是 P1。
	for _, id := range []string{"P1", " P1 "} {
		got, err := s.RegisterPoint(id, "完全不同的名称")
		if !errors.Is(err, ErrDuplicatePoint) {
			t.Fatalf("已有编号 %q 在无法保存时仍应报重复编号，got %v", id, err)
		}
		if got.ID != "" || got.Name != "" {
			t.Fatalf("重复编号登记必须返回空采样点，got %+v", got)
		}
	}
	// 空字段同样先于保存被拒绝，不冒充保存失败。
	if _, err := s.RegisterPoint("P9", "   "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("空白名称应报空字段错误，got %v", err)
	}
	// 校验被拒绝不改变任何状态。
	if got := s.points["P1"]; got.Name != "一号取水口" {
		t.Fatalf("重复编号登记不得改动原采样点名称: %+v", got)
	}
	if _, ok := s.points["P9"]; ok {
		t.Fatal("空字段登记不得留下采样点")
	}

	// 恢复正常保存条件。
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 原有采样点继续使用：新增另一项目的限值、再录一份样品都正常。
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S2", "P1", at(11, 0), Measurement{Item: "pH", Value: 6})
	// 此前的已确认样品判定依据依旧不变。
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "恢复后原样品判定")

	// 调用方先成功登记另一个编号，不能把失败的 P2 顺带保存。
	if p, err := s.RegisterPoint("P3", "三号点"); err != nil || p.ID != "P3" || p.Name != "三号点" {
		t.Fatalf("恢复后登记其它编号异常: %+v err=%v", p, err)
	}
	if _, ok := s.points["P2"]; ok {
		t.Fatal("别的成功登记不得顺带写入失败编号")
	}

	// 关闭后重新打开同一数据存放：原采样点及其限值、样品判定原样，
	// P3 已保存，失败的 P2 仍是未登记状态。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("重开后原采样点变化: %+v", got)
	}
	if got := s2.points["P3"]; got.ID != "P3" || got.Name != "三号点" {
		t.Fatalf("重开后新成功登记的采样点缺失: %+v", got)
	}
	if _, ok := s2.points["P2"]; ok {
		t.Fatal("重开后不得出现失败登记产生的采样点 P2")
	}
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 2 {
		t.Fatalf("重开后按点查询变化: %+v err=%v", reopened, err)
	}
	var reopenedS1 *Sample
	for i := range reopened {
		if reopened[i].ID == "S1" {
			reopenedS1 = &reopened[i]
		}
	}
	if reopenedS1 == nil {
		t.Fatalf("重开后原样品 S1 丢失: %+v", reopened)
	}
	// 原样品的测量值、状态、所用上限保持原样。
	checkPHExceededSaved(t, *reopenedS1, "重开后原已确认样品")
	if _, err := s2.SetLimit("P2", "pH", 1.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("重开后失败编号仍应报采样点未登记，got %v", err)
	}

	// 用失败过的编号、填写一个与失败请求不同的合法名称，作为首次登记成功接受；
	// 返回的名称只来自这次成功请求，而不是失败时填写的“二号取水口”。
	p, err := s2.RegisterPoint("P2", "  正式二号点  ")
	if err != nil {
		t.Fatalf("恢复后失败编号应能首次登记成功: %v", err)
	}
	if p.ID != "P2" || p.Name != "正式二号点" {
		t.Fatalf("成功登记应返回本次名称（去空白），got %+v", p)
	}
	if got := s2.points["P2"]; got.Name != "正式二号点" {
		t.Fatalf("保存的名称只能来自成功请求: %+v", got)
	}

	// 再次登记该编号恢复现有的重复编号拒绝，成功名称不被第二次名称替换。
	if dup, err := s2.RegisterPoint(" P2 ", "二号取水口"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("成功登记后重复编号应被拒绝，got %+v err=%v", dup, err)
	}
	if got := s2.points["P2"]; got.ID != "P2" || got.Name != "正式二号点" {
		t.Fatalf("重复登记不得替换成功名称: %+v", got)
	}
}
