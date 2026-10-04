# 本地取样与超标判定

在本机运行的本地取样与超标判定。数据存放在调用方指定的目录中，关闭后重新打开仍能查看原有记录；不同目录的数据互不混用。

## 功能

- `water.Open(dir)` / `Close`：打开（必要时创建）本地数据存放。
- `RegisterPoint(id, name)`：按编号唯一登记采样点，重复编号拒绝；编号、名称去首尾空白后保存。
- `SetLimit(point, item, value, effective)`：为采样点的测量项目登记数值上限及生效时间，支持多版与乱序补录；同一生效时间的第二条拒绝。
- `SubmitSample(id, point, sampledAt, measurements...)`：录入样品，初始待判定；同编号同内容（项目顺序无关）幂等返回原样品，内容不同整体拒绝。
- `Confirm(id)`：逐项取生效时间不晚于采样时间的最近一版上限判定，测量值大于上限才超标；缺适用上限则整次拒绝，样品保持待判定。重复确认返回已保存结果。
- `Void(id, reason)`：作废待判定或已确认样品，原因去空白后非空；相同原因幂等，不同原因拒绝。
- `ListByPoint(point)`：按采样时间从晚到早、同时刻按编号升序列出样品。
- `LatestResult(point)`：最近有效的已确认样品，无则明确返回无结果。

所有被拒绝的操作都不会留下新增或变更记录。`water.Ready` 行为保持不变。

## 使用

### 场景

同一个采样点上有一份含两个测量项目（pH、浊度）的样品，采样时间为 **2026-09-10 00:00 UTC**：

- pH 登记了一版 2026-09-01 生效的上限 `8.0`，采样当时已经适用；
- 浊度只登记了一版 **2026-09-20 才生效** 的上限 `3.0`——采样当时没有任何适用上限，哪怕存在“未来版本”也不能用；
- 测量值安排为 pH `9`（大于上限）、浊度 `4`（补录后恰好等于上限），方便对照“超标 / 达标”以及整份样品的结论。

第一次确认会因浊度缺少采样当时适用的上限而被**整次拒绝**，样品保持待判定；补录一版生效时间**不晚于**（示例中恰好等于）采样时间的上限后，对原来那份待判定样品再次确认，即可保存逐项判定依据。

下面的程序就是仓库里的 [`demo/main.go`](demo/main.go)。先指定一个**空的本地数据目录**（不存在会自动创建）运行：

```bash
go run ./demo -dir ./water-data-demo
```

> 示例面向空目录：想完整重跑一遍，请换一个新目录或清空原目录后再运行（采样点编号重复登记会被拒绝）。

### 完整示例

```go
// 这份程序对应 README“使用”一节的完整示例：
// 一份含两个测量项目的样品，因其中一个项目在采样时刻没有适用上限而被拒绝确认；
// 补录一版不晚于采样时刻生效的上限后，对原样品重新确认并保存判定结果。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/descikazuyq/water-sample/water"
)

const layout = "2006-01-02 15:04:05 Z07:00"

func must(action string, err error) {
	if err != nil {
		log.Fatalf("%s 失败: %v", action, err)
	}
}

func statusText(st water.Status) string {
	switch st {
	case water.StatusPending:
		return "待判定"
	case water.StatusConfirmed:
		return "已确认"
	case water.StatusVoided:
		return "已作废"
	default:
		return string(st)
	}
}

// printSamples 按“项目 / 测量值 / 实际使用的上限及其生效时间”打印，
// 判定依据不能只看一个超标布尔值。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（还没有判定依据）")
			continue
		}
		for _, r := range smp.Results {
			verdict := "达标"
			if r.Exceeded {
				verdict = "超标"
			}
			fmt.Printf("    逐项判定：项目 %s | 测量值 %g | 适用上限 %g（%s 生效）| %s\n",
				r.Item, r.Value, r.Limit, r.LimitEffective.UTC().Format(layout), verdict)
		}
	}
}

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo -dir ./water-data-demo")
	}

	// 1) 打开调用方指定的本地数据目录（空目录会自动创建；关闭后可重新打开）。
	store, err := water.Open(*dir)
	must("打开数据存放", err)

	const pointID = "P1"
	const sampleID = "S-20260910-01"
	sampledAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	phEffective := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) // 早于采样时间
	tFuture := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)    // 晚于采样时间
	tBackfill := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)  // 恰好等于采样时间

	// 2) 登记采样点。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)

	// 3) 登记限值：pH 有一版采样前已生效的上限；浊度“只”登记了一版
	//    采样之后才生效的上限——它存在，但这次样品用不了。
	_, err = store.SetLimit(pointID, "pH", 8.0, phEffective)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "浊度", 3.0, tFuture)
	must("登记浊度未来版本上限", err)

	// 4) 录入含两个测量项目的样品：pH 9（大于 8），浊度 4。初始状态为待判定。
	submitted, err := store.SubmitSample(sampleID, pointID, sampledAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "浊度", Value: 4},
	)
	must("录入样品", err)
	fmt.Printf("样品已录入：%s，状态 %s\n", submitted.ID, statusText(submitted.Status))

	// 5) 尝试确认。浊度在采样时刻没有“生效时间不晚于采样时间”的上限，
	//    本次确认被整体拒绝。
	rejected, err := store.Confirm(sampleID)
	if err == nil {
		log.Fatal("缺少适用上限时确认不应成功")
	}
	// 必须显式识别错误：不能忽略 err 后把返回的空样品当成成功结果。
	// err != nil 时 rejected 是零值（连编号、待判定状态都没有），不能使用。
	if !errors.Is(err, water.ErrMissingLimit) {
		log.Fatalf("预期缺少适用上限错误，实际为: %v", err)
	}
	fmt.Printf("确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作判定结果）\n",
		rejected.ID, rejected.Status)

	// 6) 按采样点查看：样品仍是“待判定”，没有任何逐项结果；
	//    超标标记 false 只是零值，含义是“没有结论”，不是“达标”。
	list, err := store.ListByPoint(pointID)
	must("按采样点查询", err)
	fmt.Println("被拒绝后按采样点查询：")
	printSamples(list)
	if list[0].Status != water.StatusPending || len(list[0].Results) != 0 {
		log.Fatal("被拒绝的确认不得留下单项结果或改变待判定状态")
	}

	// 7) 为缺失的浊度补录一版上限，生效时间恰好等于采样时间。
	//    生效时间不晚于采样时间即可采用，恰好相等时采用的正是这一版。
	_, err = store.SetLimit(pointID, "浊度", 4.0, tBackfill)
	must("补录浊度上限", err)

	// 8) 对“原来那份待判定样品”重新确认（样品编号不变，不需要重新录入）。
	confirmed, err := store.Confirm(sampleID)
	must("补录后重新确认", err)
	fmt.Println("补录后重新确认成功，已保存的判定依据：")
	printSamples([]water.Sample{confirmed})
	if confirmed.Status != water.StatusConfirmed {
		log.Fatalf("样品状态应为已确认，实际为 %s", confirmed.Status)
	}
	overall := "达标"
	if confirmed.Exceeded {
		overall = "超标"
	}
	fmt.Printf("整份样品结论：%s（一项超标即整份超标；等于上限算达标）\n", overall)

	// 9) 再次按采样点查询，与失败时的待判定记录对照：状态已是已确认，
	//    并带上了每个项目实际使用的上限和生效时间。
	list, err = store.ListByPoint(pointID)
	must("确认后按采样点查询", err)
	fmt.Println("确认成功后按采样点查询：")
	printSamples(list)

	// 10) 关闭后重新打开同一目录，记录仍在（不同目录的数据互不混用）。
	must("关闭数据存放", store.Close())
	reopened, err := water.Open(*dir)
	must("重新打开数据存放", err)
	defer func() {
		if err := reopened.Close(); err != nil {
			log.Printf("关闭重新打开的数据存放: %v", err)
		}
	}()
	list, err = reopened.ListByPoint(pointID)
	must("重新打开后按采样点查询", err)
	fmt.Println("关闭后重新打开同一目录，记录仍在：")
	printSamples(list)
}
```

### 示例输出

被拒绝时样品仍是“待判定”，没有单项结果，也没有整份结论；补录并重新确认后，每条结果都能对应到项目、测量值、实际使用的上限及其生效时间：

```text
样品已录入：S-20260910-01，状态 待判定
确认被拒绝：water: 存在找不到适用上限的测量项目: 样品 S-20260910-01 的项目 浊度
（出错时返回值是空样品：编号 ""，状态 ""，不能当作判定结果）
被拒绝后按采样点查询：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 待判定 | 超标标记 false
    逐项判定：无（还没有判定依据）
补录后重新确认成功，已保存的判定依据：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 4 | 适用上限 4（2026-09-10 00:00:00 Z 生效）| 达标
整份样品结论：超标（一项超标即整份超标；等于上限算达标）
确认成功后按采样点查询：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 4 | 适用上限 4（2026-09-10 00:00:00 Z 生效）| 达标
关闭后重新打开同一目录，记录仍在：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 4 | 适用上限 4（2026-09-10 00:00:00 Z 生效）| 达标
```

### 要点

- **缺适用上限是整次拒绝，不是部分判定。** `Confirm` 返回 `errors.Is(err, water.ErrMissingLimit)` 可识别的错误，此时返回的样品是零值，样品保持 `StatusPending`，不会留下任何单项结果，也不会出现整份达标结论。不能忽略 `err` 后把返回值当成功结果使用。
- **只看生效时间不晚于采样时间的最近一版。** 浊度的 `3.0@2026-09-20` 晚于采样时间，这次样品用不了；补录的 `4.0@2026-09-10` 恰好等于采样时间，边界时刻采用该版。
- **补录后对原样品再确认即可**，不需要重新录入样品；确认成功后结果随每次成功变更落盘，`Close` 后重新 `Open` 同一目录仍可查询。
- **按采样点查询要结合状态读结论。** 待判定记录的 `Exceeded == false` 只是尚无结论的零值，**不表示达标**；已确认记录的 `Exceeded` 才是整份样品的结论，逐项依据见 `Results`（项目、测量值、上限、上限生效时间、单项是否超标）。测量值**大于**上限才超标，恰好等于上限算达标；任一项目超标则整份样品超标。

### 运行测试

```bash
go test ./...
```

## 样品作废

`Void(id, reason)` 作废一份待判定或已确认的样品。作废**取消该样品作为有效结果的资格**，但**不会删除已经保存的判定依据**：已确认样品作废后，它的测量值、逐项判定（项目、测量值、实际使用的上限及其生效时间、单项结论）和整份超标标记都原样保留在记录里，只能用于核对历史，不再代表当前有效结论。待判定样品作废前没有判定依据，作废后也不会凭空获得结论。

因此查询时要区分两类信息：

- **保留的历史记录**：`ListByPoint` 按采样点列出全部样品，已作废的记录仍在其中，带着作废状态、作废原因和作废前保存的判定依据；
- **当前有效的判定结果**：`LatestResult` 只在已确认且未作废的样品中取最近一份，已作废和待判定的一律跳过。若该采样点没有其他已确认且未作废的样品，`LatestResult` 以 `ok == false` 明确表示无结果——不能把空返回值或待判定记录中为 `false` 的超标标记解释成达标。这与按点列表仍能查到作废记录并不矛盾。

对已作废的样品再次请求 `Confirm` 会被明确拒绝（`errors.Is(err, water.ErrVoided)`），拒绝后原作废状态、作废原因和历史判定依据继续保留。`Void` 的原因去掉首尾空白后不能为空：仅含空白的原因会被拒绝（`errors.Is(err, water.ErrEmptyField)`），不能当作成功操作。任何调用失败都要先处理错误——出错时返回的是零值空样品，不能把它打印成确认或作废成功。

### 场景

同一个采样点上有两份**已确认**样品，测量项目都是 pH 和浊度（两版上限均 2026-09-01 生效，pH `8.0`、浊度 `3.0`）：

- 较早的 `S-20260905-01`（2026-09-05 采样）：pH `7`、浊度 `2`，均在限值内，整份**达标**；
- 较晚的 `S-20260910-01`（2026-09-10 采样）：pH `9` 大于上限，整份**超标**。

作废前最近有效结果指向较晚的超标样品。随后为较晚样品填写非空原因作废，再按采样点查看记录、查询最近有效结果，并对已作废样品再次请求确认。

下面的程序就是仓库里的 [`demo-void/main.go`](demo-void/main.go)。先指定一个**空的本地数据目录**（不存在会自动创建）运行：

```bash
go run ./demo-void -dir ./water-data-void-demo
```

> 示例面向空目录：想完整重跑一遍，请换一个新目录或清空原目录后再运行（采样点编号重复登记会被拒绝）。

### 完整示例

```go
// 这份程序对应 README“样品作废”一节的完整示例：
// 同一采样点的两份已确认样品，较早一份达标、较晚一份超标，作废前最近有效结果指向较晚那份；
// 为较晚样品填写非空原因作废后，按点列表仍保留它的测量值与逐项判定依据（仅供核对历史），
// 而最近有效结果退回到较早的达标样品；对已作废样品再次确认会被明确拒绝。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/descikazuyq/water-sample/water"
)

const layout = "2006-01-02 15:04:05 Z07:00"

func must(action string, err error) {
	if err != nil {
		log.Fatalf("%s 失败: %v", action, err)
	}
}

func statusText(st water.Status) string {
	switch st {
	case water.StatusPending:
		return "待判定"
	case water.StatusConfirmed:
		return "已确认"
	case water.StatusVoided:
		return "已作废"
	default:
		return string(st)
	}
}

// printSamples 按“状态 / 作废原因 / 逐项判定依据”打印。
// 已作废记录里的超标标记和逐项判定是作废前保存的历史，只用于核对，不是当前有效结论。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if smp.Status == water.StatusVoided {
			fmt.Printf("    作废原因：%s\n", smp.VoidReason)
		}
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（还没有判定依据）")
			continue
		}
		for _, r := range smp.Results {
			verdict := "达标"
			if r.Exceeded {
				verdict = "超标"
			}
			fmt.Printf("    逐项判定：项目 %s | 测量值 %g | 适用上限 %g（%s 生效）| %s\n",
				r.Item, r.Value, r.Limit, r.LimitEffective.UTC().Format(layout), verdict)
		}
	}
}

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo-void -dir ./water-data-void-demo")
	}

	// 1) 打开调用方指定的本地数据目录（空目录会自动创建；关闭后可重新打开）。
	store, err := water.Open(*dir)
	must("打开数据存放", err)
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭数据存放: %v", err)
		}
	}()

	const pointID = "P1"
	const earlyID = "S-20260905-01" // 较早采样，达标
	const lateID = "S-20260910-01"  // 较晚采样，pH 超标
	effective := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	earlyAt := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	lateAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// 2) 登记采样点，并为两个项目各登记一版采样前已生效的上限。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)
	_, err = store.SetLimit(pointID, "pH", 8.0, effective)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "浊度", 3.0, effective)
	must("登记浊度上限", err)

	// 3) 录入并确认较早的样品：pH 7、浊度 2，均在限值内，整份达标。
	_, err = store.SubmitSample(earlyID, pointID, earlyAt,
		water.Measurement{Item: "pH", Value: 7},
		water.Measurement{Item: "浊度", Value: 2},
	)
	must("录入较早样品", err)
	_, err = store.Confirm(earlyID)
	must("确认较早样品", err)

	// 4) 录入并确认较晚的样品：pH 9 大于上限 8，整份超标。
	_, err = store.SubmitSample(lateID, pointID, lateAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "浊度", Value: 2},
	)
	must("录入较晚样品", err)
	_, err = store.Confirm(lateID)
	must("确认较晚样品", err)

	// 5) 作废前，最近有效结果是较晚采样的超标样品。
	latest, ok, err := store.LatestResult(pointID)
	must("作废前查询最近有效结果", err)
	if !ok {
		log.Fatal("作废前应有最近有效结果")
	}
	fmt.Println("作废前最近有效结果（指向较晚的超标样品）：")
	printSamples([]water.Sample{latest})

	// 6) 作废原因仅含空白时同样被拒绝，这不是成功操作。
	_, err = store.Void(lateID, "   ")
	if err == nil {
		log.Fatal("空白作废原因不应成功")
	}
	if !errors.Is(err, water.ErrEmptyField) {
		log.Fatalf("预期空字段错误，实际为: %v", err)
	}
	fmt.Printf("空白原因作废被拒绝：%v\n", err)

	// 7) 填写非空原因作废较晚的超标样品。作废取消它作为有效结果的资格，
	//    但不删除已保存的测量值与逐项判定依据。
	voided, err := store.Void(lateID, "采样瓶破损，等待复测重采")
	must("作废较晚样品", err)
	fmt.Println("作废成功，作废后的样品记录（判定依据保留，仅供核对历史）：")
	printSamples([]water.Sample{voided})

	// 8) 按采样点查看：两份样品都在，已作废的较晚样品仍带着
	//    作废前保存的测量值、所用上限、生效时间和逐项结论。
	list, err := store.ListByPoint(pointID)
	must("作废后按采样点查询", err)
	fmt.Println("作废后按采样点查询（较晚样品仍在列表中，历史依据保留）：")
	printSamples(list)

	// 9) 最近有效结果退回到较早的已确认达标样品：
	//    不能因为作废记录里仍有判定内容就继续选中它。
	latest, ok, err = store.LatestResult(pointID)
	must("作废后查询最近有效结果", err)
	if !ok {
		log.Fatal("较早样品仍已确认且未作废，应有最近有效结果")
	}
	fmt.Println("作废后最近有效结果（退回较早的达标样品）：")
	printSamples([]water.Sample{latest})
	if latest.ID != earlyID {
		log.Fatalf("最近有效结果应为 %s，实际为 %s", earlyID, latest.ID)
	}

	// 10) 对已作废样品再次请求确认，被明确拒绝。
	//     必须先处理错误：err != nil 时返回的是空样品，不能打印成确认成功。
	reconfirmed, err := store.Confirm(lateID)
	if err == nil {
		log.Fatal("已作废样品不应再确认成功")
	}
	if !errors.Is(err, water.ErrVoided) {
		log.Fatalf("预期已作废错误，实际为: %v", err)
	}
	fmt.Printf("对已作废样品再次确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作确认成功）\n",
		reconfirmed.ID, reconfirmed.Status)

	// 11) 拒绝不会改动记录：原作废状态、原因和历史判定依据继续保留。
	list, err = store.ListByPoint(pointID)
	must("拒绝后按采样点查询", err)
	fmt.Println("再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：")
	printSamples(list)

	// 12) 一个没有任何已确认且未作废样品的采样点，最近有效结果明确返回无结果。
	//     ok 为 false 才是“无结果”的表达方式；不能把空返回值或待判定记录里
	//     为 false 的超标标记解释成达标。
	_, err = store.RegisterPoint("P2", "二号取水口")
	must("登记第二个采样点", err)
	_, ok, err = store.LatestResult("P2")
	must("查询无结果采样点", err)
	fmt.Printf("采样点 P2 没有任何已确认样品：最近有效结果 ok=%v（明确表示无结果，不是达标）\n", ok)
}
```

### 示例输出

作废的是较晚的 `S-20260910-01`：它在按点列表里仍是第一条（按采样时间从晚到早），作废状态、原因和作废前保存的逐项判定依据都在；而最近有效结果退回较早的 `S-20260905-01`，两份样品的测量与结论互不混淆：

```text
作废前最近有效结果（指向较晚的超标样品）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
空白原因作废被拒绝：water: 编号、名称或项目去掉首尾空白后不能为空
作废成功，作废后的样品记录（判定依据保留，仅供核对历史）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：采样瓶破损，等待复测重采
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
作废后按采样点查询（较晚样品仍在列表中，历史依据保留）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：采样瓶破损，等待复测重采
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
作废后最近有效结果（退回较早的达标样品）：
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
对已作废样品再次确认被拒绝：water: 样品已作废，不能再确认: S-20260910-01
（出错时返回值是空样品：编号 ""，状态 ""，不能当作确认成功）
再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：采样瓶破损，等待复测重采
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 浊度 | 测量值 2 | 适用上限 3（2026-09-01 00:00:00 Z 生效）| 达标
采样点 P2 没有任何已确认样品：最近有效结果 ok=false（明确表示无结果，不是达标）
```

### 要点

- **作废保留历史，不删除依据。** 已确认样品作废后，`ListByPoint` 里仍能查到它，状态为已作废并带作废原因，作废前保存的测量值、所用上限、生效时间和逐项结论原样保留；这些达标或超标信息只能用于核对历史。待判定样品作废前没有判定依据，作废后也不会凭空获得结论。
- **当前有效结果只看已确认且未作废的样品。** `LatestResult` 跳过待判定与已作废记录：较晚的超标样品作废后，最近有效结果退回较早的达标样品，不能因为作废记录里仍有判定内容就继续选中它，也不要把两份样品的测量与结论混在一起读。
- **无结果就是 `ok == false`。** 若该采样点没有其他已确认且未作废的样品，`LatestResult` 明确返回无结果；不能把空返回值或待判定记录中为 `false` 的超标标记解释成达标。这与按点列表仍能查到作废记录并不矛盾。
- **已作废样品不能再确认。** 再次 `Confirm` 返回 `errors.Is(err, water.ErrVoided)` 可识别的错误，原作废状态、原因和历史依据继续保留；出错时返回的是零值空样品，必须先处理错误，不能打印成确认成功。
- **作废原因必须非空。** 仅含空白的原因返回 `errors.Is(err, water.ErrEmptyField)`，同样不是成功操作；相同原因重复作废幂等返回原记录，不同原因拒绝（`errors.Is(err, water.ErrVoidReasonConflict)`）。
```
