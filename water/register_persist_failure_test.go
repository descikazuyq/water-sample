package water

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 本文件的测试只针对一件事：RegisterPoint 提交的编号此前没有登记过、名称合法，
// 但本地数据无法保存时，这次登记必须整体失败——返回保存错误和编号、名称均为空的
// 采样点，不占用编号、不留下任何记录。保护“登记被拒绝就不留下记录”的现有行为：
// 失败的编号不能成为可登记限值的已知采样点，不能随其它成功操作被一并落盘；
// 保存条件恢复后该编号仍按首次登记接受，去首尾空白规则与重复编号拒绝保持不变；
// 保存失败与内容校验失败（如重复编号）必须明确区分。

// 第一次使用空数据存放时登记失败：返回保存错误与空采样点，编号不被占用；
// 恢复后先成功登记另一个编号并重开数据存放，失败编号仍是未登记，
// 再用带首尾空白的同一编号首次登记成功，之后恢复重复编号拒绝。
func TestRegisterPointPersistFailureEmptyStore(t *testing.T) {
	s, dir := open(t)

	// 让落盘失败：把临时文件路径占成目录，WriteFile 必然失败
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 全新编号、合法名称：必须返回保存错误，不能冒充内容校验失败
	got, err := s.RegisterPoint("P9", "九号取水口")
	if err == nil {
		t.Fatal("RegisterPoint should fail when the new point cannot be saved")
	}
	for _, target := range []error{ErrEmptyField, ErrDuplicatePoint, ErrInvalidText} {
		if errors.Is(err, target) {
			t.Fatalf("save failure must not be reported as %v: %v", target, err)
		}
	}
	// 返回编号、名称均为空的采样点，调用方不能当登记成功使用
	if got.ID != "" || got.Name != "" {
		t.Fatalf("failed save must return an empty point, got %+v", got)
	}
	// 内存状态同步回滚：编号不能因此被占用
	if _, ok := s.points["P9"]; ok {
		t.Fatal("failed registration must be rolled back from the in-memory store")
	}
	// 失败编号不能成为可登记限值的已知采样点
	if _, err := s.SetLimit("P9", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("failed id must stay unregistered, got %v", err)
	}

	// 恢复正常保存条件
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 先成功登记另一个编号：这次成功保存不能把失败编号顺带落盘
	mustPoint(t, s, "PA", "对照点")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// 重开后失败编号仍是未登记，不能随别的成功操作被一并保存
	if _, ok := s2.points["P9"]; ok {
		t.Fatal("failed id must not be persisted by another successful operation")
	}
	if _, err := s2.SetLimit("P9", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("reopened: failed id should stay unregistered, got %v", err)
	}

	// 用失败过的编号、换一个与失败请求不同的合法名称，并加首尾空白：
	// 判断的仍是同一个编号，应作为首次登记成功接受，
	// 返回的编号和名称只来自这次成功请求（去掉首尾空白后的内容）。
	p, err := s2.RegisterPoint("  P9  ", "  九号排放口  ")
	if err != nil {
		t.Fatalf("failed id should be accepted as a first registration: %v", err)
	}
	if p.ID != "P9" || p.Name != "九号排放口" {
		t.Fatalf("returned point must come from this successful request, trimmed: %+v", p)
	}

	// 再次登记该编号：恢复现有的重复编号拒绝行为，成功名称不被替换
	if _, err := s2.RegisterPoint("P9", "九号取水口"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("duplicate id should be rejected again, got %v", err)
	}
	if got := s2.points["P9"]; got.Name != "九号排放口" {
		t.Fatalf("successful name must not be replaced: %+v", got)
	}
}

// 数据中已有成功登记的采样点时登记失败：原采样点的编号和名称保持不变，
// 已关联到它的限值和样品仍归属于它，按点查询看到的样品及已保存判定依据
// 保持原样；失败登记不影响原采样点继续使用。同样无法保存的条件下，
// 提交已有编号仍返回现有的重复编号错误，不说成保存失败。
func TestRegisterPointPersistFailureKeepsExistingData(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "前置确认")

	// 只让新登记的保存失败：采样点、限值和样品此前均已成功落盘
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 全新编号、合法名称：返回保存错误和空采样点
	got, err := s.RegisterPoint("P9", "九号取水口")
	if err == nil {
		t.Fatal("RegisterPoint should fail when the new point cannot be saved")
	}
	for _, target := range []error{ErrEmptyField, ErrDuplicatePoint, ErrInvalidText} {
		if errors.Is(err, target) {
			t.Fatalf("save failure must not be reported as %v: %v", target, err)
		}
	}
	if got.ID != "" || got.Name != "" {
		t.Fatalf("failed save must return an empty point, got %+v", got)
	}
	if _, ok := s.points["P9"]; ok {
		t.Fatal("failed registration must be rolled back from the in-memory store")
	}
	// 失败编号不能成为可登记限值的已知采样点
	if _, err := s.SetLimit("P9", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("failed id must stay unregistered, got %v", err)
	}

	// 保存失败与内容校验失败明确区分：同样无法保存的条件下，
	// 提交已有编号仍返回现有的重复编号错误，不把它说成保存失败
	dup, err := s.RegisterPoint("P1", "完全不同的名称")
	if !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("existing id must keep the duplicate rejection, got %v", err)
	}
	if dup.ID != "" || dup.Name != "" {
		t.Fatalf("rejected duplicate must return an empty point, got %+v", dup)
	}

	// 原采样点的编号和名称保持不变，已关联的限值仍归属于它
	if got := s.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("existing point must not be touched by the failed registration: %+v", got)
	}
	versions := s.limits[limitGroup{pointID: "P1", item: "pH"}]
	if len(versions) != 1 || versions[0].Value != 8.0 {
		t.Fatalf("existing limit must stay attached to the original point: %+v", versions)
	}

	// 按点查询看到的样品及已保存判定依据保持原样：测量值、状态、所用上限不变
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint after failed save: %+v err=%v", list, err)
	}
	checkPHExceededSaved(t, list[0], "保存失败后按点查看")
	// 原采样点继续正常使用：重复确认仍返回原结论，最近有效结果仍是它
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "保存失败后重复确认")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("latest valid result should stay S1, got %+v ok=%v err=%v", latest, ok, err)
	}

	// 恢复正常保存条件
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 原采样点继续可用：补录限值、录入新样品照常成功
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S2", "P1", at(11, 0), Measurement{Item: "COD", Value: 20})

	// 先成功登记另一个编号，再关闭并重开同一数据存放：
	// 失败编号不能随着别的成功操作被一并保存
	mustPoint(t, s, "PA", "对照点")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if _, ok := s2.points["P9"]; ok {
		t.Fatal("failed id must not be persisted by another successful operation")
	}
	if _, err := s2.SetLimit("P9", "pH", 8.0, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("reopened: failed id should stay unregistered, got %v", err)
	}
	// 重开后原采样点及其样品、判定依据保持原样
	if got := s2.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("reopened: original point changed: %+v", got)
	}
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 2 {
		t.Fatalf("reopened ListByPoint: %+v err=%v", reopened, err)
	}
	checkPHExceededSaved(t, reopened[1], "重开后按点查看")

	// 用失败过的编号、换一个与失败请求不同的合法名称并加首尾空白：
	// 仍按同一编号首次登记成功，返回去掉首尾空白后的本次内容
	p, err := s2.RegisterPoint("  P9  ", "  九号排放口  ")
	if err != nil {
		t.Fatalf("failed id should be accepted as a first registration: %v", err)
	}
	if p.ID != "P9" || p.Name != "九号排放口" {
		t.Fatalf("returned point must come from this successful request, trimmed: %+v", p)
	}

	// 再次登记该编号：恢复现有的重复编号拒绝行为，成功名称不被第二次名称替换
	if _, err := s2.RegisterPoint("P9", "九号取水口"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("duplicate id should be rejected again, got %v", err)
	}
	if got := s2.points["P9"]; got.Name != "九号排放口" {
		t.Fatalf("successful name must not be replaced: %+v", got)
	}
}
