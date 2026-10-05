# 本地取样与超标判定

在本机运行的本地取样与超标判定。数据存放在调用方指定的目录中，关闭后重新打开仍能查看原有记录；不同目录的数据互不混用。

## 功能

- `water.Open(dir)` / `Close`：打开（必要时创建）本地数据存放。打开时会核对已确认样品的原测量与逐项判定是否完整一一对应，对不上则整次打开失败（详见[打开时的记录完整性校验](#打开时的记录完整性校验)）。
- `RegisterPoint(id, name)`：按编号唯一登记采样点，重复编号拒绝；编号、名称去首尾空白后保存。
- `SetLimit(point, item, value, effective)`：为采样点的测量项目登记数值上限及生效时间，支持多版与乱序补录；同一生效时间的第二条拒绝。
- `SubmitSample(id, point, sampledAt, measurements...)`：录入样品，初始待判定；同编号同内容（项目顺序无关）幂等返回原样品，内容不同整体拒绝。
- `Confirm(id)`：逐项取生效时间不晚于采样时间的最近一版上限判定，测量值大于上限才超标；缺适用上限则整次拒绝，样品保持待判定。重复确认返回已保存结果。
- `Void(id, reason)`：作废待判定或已确认样品，原因去空白后非空；相同原因幂等，不同原因拒绝。作废取消样品作为有效结果的资格，但不删除记录和已保存的判定依据（详见[样品作废](#样品作废)）。
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

作废回答的是“这份样品**还算不算数**”，而不是“把这份样品从记录里抹掉”：

- `Void(id, reason)` 会把样品置为 `StatusVoided`，**取消它作为有效结果的资格**——`LatestResult` 从此跳过它，对它再调 `Confirm` 会返回可被 `errors.Is(err, water.ErrVoided)` 识别的错误。
- 作废**不会删除**已经保存的任何内容：`ListByPoint` 仍按原排列列出这份样品，作废前保存的测量值、逐项所用上限与生效时间、逐项达标/超标结论、整份超标标记，以及作废原因都原样保留。
- 因此保留下来的达标或超标信息**只能用于核对历史**，不是当前有效的判定结论。读记录时要先看 `Status`：`已确认` 记录里的 `Exceeded` 是当前结论；`已作废` 记录里即使 `Exceeded == true`、`Results` 完整，也不再代表该采样点的有效结果。两个查询的分工是：**按点列表是完整历史台账（含作废），最近有效结果只认已确认且未作废的样品**，两者并不矛盾。
- 待判定样品作废前本来就没有判定依据；作废只改状态、记原因，**不会凭空生成结论**，作废后它的 `Results` 仍为空、`Exceeded` 仍为假。若该采样点没有其他已确认且未作废的样品，`LatestResult` 返回空样品、`ok == false` 且不报错，**明确表示“无结果”**——这既不是达标也不是超标，不能把空返回值或待判定/作废记录中为假的超标标记解释成达标。
- 作废原因去掉首尾空白后必须非空：仅含空白（`""`、`"   "`）时整次拒绝，可被 `errors.Is(err, water.ErrEmptyField)` 识别。任何调用失败都要**先处理 `err`**：失败时返回的是空样品，不能把它打印成作废或确认成功。重复作废时，处理后相同的原因幂等返回原记录，不同原因以 `water.ErrVoidReasonConflict` 拒绝。

### 场景

同一个采样点 `P1` 上有两份样品，pH 与 COD 的上限均为 `8.0`、`30.0`，2026-09-01 生效：

- **较早样品** `S-20260905-01`，2026-09-05 采样：pH `7`、COD `20`，两项达标，已确认；
- **较晚样品** `S-20260910-01`，2026-09-10 采样：pH `9`（大于上限，超标）、COD `30`（恰好等于上限，达标），整份超标，已确认。

作废前最近有效结果指向较晚样品。随后用非空原因把较晚样品作废：按点列表里它仍在、原测量与判定依据都保留；最近有效结果则退到较早的达标样品，两份样品的数据不会混在一起。之后再对它请求确认会被明确拒绝，作废状态、原因和历史依据不受影响。另一个采样点 `P2` 只有一份**从未确认**就作废的样品：列表可查但没有逐项判定，最近有效结果明确为“无结果”。

下面的程序就是仓库里的 [`demo-void/main.go`](demo-void/main.go)，同样指定一个**空的本地数据目录**运行：

```bash
go run ./demo-void -dir ./water-data-void-demo
```

### 完整示例

```go
// 这份程序对应 README“样品作废”一节的完整示例：
// 同一采样点上两份已确认样品——较早的一份达标，较晚的一份至少一项超标；
// 作废较晚样品后，按点列表仍保留它的原测量与逐项判定依据（仅供核对历史），
// 最近有效结果则退到较早的已确认样品。另一个采样点只有一份未确认即作废的
// 待判定样品，用来说明作废不会凭空生成结论，且没有其他有效样品时
// 最近有效结果明确为“无结果”，不能把空返回或为假的超标标记解释成达标。
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

// printSamples 打印状态、超标标记、作废原因以及逐项判定依据。
// 作废记录的历史依据和有效样品的当前判定用的是同一套字段，
// 读者需要结合“状态”区分两者，不能只看超标标记。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if smp.VoidReason != "" {
			fmt.Printf("    作废原因：%s\n", smp.VoidReason)
		}
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（没有已保存的判定依据）")
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

// printLatest 打印最近有效结果；没有已确认且未作废的样品时明确打印“无结果”，
// 不把空样品或记录里为假的超标标记解释成达标。
func printLatest(store *water.Store, pointID string) {
	latest, ok, err := store.LatestResult(pointID)
	must("查询最近有效结果", err)
	if !ok {
		fmt.Printf("  采样点 %s 的最近有效结果：无（没有已确认且未作废的样品；返回空样品编号 %q、状态 %q）\n",
			pointID, latest.ID, latest.Status)
		fmt.Println("  注意：这既不是达标也不是超标，不能把空返回或待判定记录的超标标记 false 当成达标。")
		return
	}
	fmt.Printf("  采样点 %s 的最近有效结果：\n", pointID)
	printSamples([]water.Sample{latest})
}

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo-void -dir ./water-data-void-demo")
	}

	store, err := water.Open(*dir)
	must("打开数据存放", err)
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭数据存放: %v", err)
		}
	}()

	const pointID = "P1"
	const earlierID = "S-20260905-01"
	const laterID = "S-20260910-01"
	eff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	earlierAt := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	laterAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// 1) 登记采样点和两版上限：pH 8、COD 30，均在 2026-09-01 生效。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)
	_, err = store.SetLimit(pointID, "pH", 8.0, eff)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "COD", 30.0, eff)
	must("登记 COD 上限", err)

	// 2) 较早样品（9/5 采样）：pH 7、COD 20，两项都不超标。
	_, err = store.SubmitSample(earlierID, pointID, earlierAt,
		water.Measurement{Item: "pH", Value: 7},
		water.Measurement{Item: "COD", Value: 20},
	)
	must("录入较早样品", err)
	earlier, err := store.Confirm(earlierID)
	must("确认较早样品", err)

	// 3) 较晚样品（9/10 采样）：pH 9 大于上限 8（超标），
	//    COD 30 恰好等于上限 30（达标），任一项目超标则整份样品超标。
	_, err = store.SubmitSample(laterID, pointID, laterAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "COD", Value: 30},
	)
	must("录入较晚样品", err)
	later, err := store.Confirm(laterID)
	must("确认较晚样品", err)
	fmt.Println("两份样品均已确认（按采样时间从晚到早排列）：")
	printSamples([]water.Sample{later, earlier})

	// 4) 作废前，最近有效结果指向采样时间最晚的已确认样品，即较晚的超标样品。
	fmt.Println("作废前查询最近有效结果：")
	printLatest(store, pointID)

	// 5) 作废原因去掉首尾空白后不能为空。仅含空白的原因直接拒绝，
	//    返回空样品——先处理 err，不能把空样品当作作废成功。
	blank, err := store.Void(laterID, "   ")
	if err == nil {
		log.Fatal("作废原因仅含空白时不应成功")
	}
	if !errors.Is(err, water.ErrEmptyField) {
		log.Fatalf("预期空白字段错误，实际为: %v", err)
	}
	fmt.Printf("空白原因作废被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作作废成功）\n",
		blank.ID, blank.Status)

	// 6) 填写非空原因（两端空白会被去掉）后作废较晚样品。
	voided, err := store.Void(laterID, "  复测确认样品污染  ")
	must("作废较晚样品", err)
	fmt.Println("填写非空原因后作废成功，返回的作废记录：")
	printSamples([]water.Sample{voided})

	// 7) 按点列表：两份样品都在。较晚样品状态为已作废并带原因，
	//    作废前保存的测量值、所用上限、生效时间和逐项结论原样保留。
	list, err := store.ListByPoint(pointID)
	must("作废后按采样点查询", err)
	fmt.Println("作废后按采样点查询（作废记录仍列出，历史依据保留，仅供核对历史）：")
	printSamples(list)

	// 8) 最近有效结果跳过已作废样品，退到较早的达标样品；
	//    返回内容整体来自较早样品，不能混入较晚样品的 pH 9 等任何数据。
	fmt.Println("作废后查询最近有效结果（退到较早的已确认样品）：")
	printLatest(store, pointID)

	// 9) 对已作废样品再次确认：明确拒绝，返回空样品。
	reconfirmed, err := store.Confirm(laterID)
	if err == nil {
		log.Fatal("已作废样品再次确认不应成功")
	}
	if !errors.Is(err, water.ErrVoided) {
		log.Fatalf("预期样品已作废错误，实际为: %v", err)
	}
	fmt.Printf("对已作废样品再次确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作确认成功）\n",
		reconfirmed.ID, reconfirmed.Status)

	// 10) 拒绝不改变任何记录：作废状态、原因与历史依据继续保留。
	list, err = store.ListByPoint(pointID)
	must("拒绝后按采样点查询", err)
	fmt.Println("再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：")
	printSamples(list)
	var kept water.Sample
	for _, smp := range list {
		if smp.ID == laterID {
			kept = smp
		}
	}
	if kept.Status != water.StatusVoided || kept.VoidReason != "复测确认样品污染" ||
		!kept.Exceeded || len(kept.Results) != 2 {
		log.Fatalf("拒绝确认不得改变作废记录: %+v", kept)
	}

	// 11) 另一个采样点：一份录入后从未确认、直接作废的待判定样品。
	const point2ID = "P2"
	const pendingID = "S-20260912-01"
	pendingAt := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	_, err = store.RegisterPoint(point2ID, "二号取水口")
	must("登记第二个采样点", err)
	_, err = store.SetLimit(point2ID, "pH", 8.0, eff)
	must("登记第二个采样点的 pH 上限", err)
	pending, err := store.SubmitSample(pendingID, point2ID, pendingAt,
		water.Measurement{Item: "pH", Value: 7})
	must("录入待判定样品", err)
	fmt.Printf("另一份样品录入后不确认直接作废（录入时状态为 %s，尚无判定依据）：\n",
		statusText(pending.Status))
	voidedPending, err := store.Void(pendingID, "录入信息有误")
	must("作废待判定样品", err)
	printSamples([]water.Sample{voidedPending})
	if len(voidedPending.Results) != 0 || voidedPending.Exceeded {
		log.Fatalf("待判定样品作废后不应凭空获得结论: %+v", voidedPending)
	}

	// 12) 按点列表能查到这份作废记录，但它没有逐项判定；该点没有其他
	//     已确认且未作废的样品，最近有效结果明确表示“无结果”。
	list2, err := store.ListByPoint(point2ID)
	must("第二个采样点按点查询", err)
	fmt.Println("第二个采样点按点查询（作废记录可查，但没有逐项判定）：")
	printSamples(list2)
	fmt.Println("第二个采样点查询最近有效结果：")
	printLatest(store, point2ID)
}
```

### 示例输出

作废后较晚样品仍完整出现在按点列表中（状态 `已作废`、带原因，pH `9 > 8` 的超标依据照旧保留），但它已不是有效结果：最近有效结果整体退到较早的达标样品，测量值与结论没有任何混拼。再次确认被明确拒绝且不改变记录；从未确认就作废的样品没有逐项判定，其所在采样点的最近有效结果明确为“无结果”：

```text
两份样品均已确认（按采样时间从晚到早排列）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 COD | 测量值 30 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 COD | 测量值 20 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
作废前查询最近有效结果：
  采样点 P1 的最近有效结果：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已确认 | 超标标记 true
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 COD | 测量值 30 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
空白原因作废被拒绝：water: 编号、名称或项目去掉首尾空白后不能为空
（出错时返回值是空样品：编号 ""，状态 ""，不能当作作废成功）
填写非空原因后作废成功，返回的作废记录：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：复测确认样品污染
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 COD | 测量值 30 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
作废后按采样点查询（作废记录仍列出，历史依据保留，仅供核对历史）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：复测确认样品污染
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 COD | 测量值 30 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 COD | 测量值 20 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
作废后查询最近有效结果（退到较早的已确认样品）：
  采样点 P1 的最近有效结果：
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 COD | 测量值 20 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
对已作废样品再次确认被拒绝：water: 样品已作废，不能再确认: S-20260910-01
（出错时返回值是空样品：编号 ""，状态 ""，不能当作确认成功）
再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：
  样品 S-20260910-01 | 采样点 P1 | 采样时间 2026-09-10 00:00:00 Z | 状态 已作废 | 超标标记 true
    作废原因：复测确认样品污染
    逐项判定：项目 pH | 测量值 9 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 超标
    逐项判定：项目 COD | 测量值 30 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
  样品 S-20260905-01 | 采样点 P1 | 采样时间 2026-09-05 00:00:00 Z | 状态 已确认 | 超标标记 false
    逐项判定：项目 pH | 测量值 7 | 适用上限 8（2026-09-01 00:00:00 Z 生效）| 达标
    逐项判定：项目 COD | 测量值 20 | 适用上限 30（2026-09-01 00:00:00 Z 生效）| 达标
另一份样品录入后不确认直接作废（录入时状态为 待判定，尚无判定依据）：
  样品 S-20260912-01 | 采样点 P2 | 采样时间 2026-09-12 00:00:00 Z | 状态 已作废 | 超标标记 false
    作废原因：录入信息有误
    逐项判定：无（没有已保存的判定依据）
第二个采样点按点查询（作废记录可查，但没有逐项判定）：
  样品 S-20260912-01 | 采样点 P2 | 采样时间 2026-09-12 00:00:00 Z | 状态 已作废 | 超标标记 false
    作废原因：录入信息有误
    逐项判定：无（没有已保存的判定依据）
第二个采样点查询最近有效结果：
  采样点 P2 的最近有效结果：无（没有已确认且未作废的样品；返回空样品编号 ""、状态 ""）
  注意：这既不是达标也不是超标，不能把空返回或待判定记录的超标标记 false 当成达标。
```

### 作废要点

- **作废取消资格，不删除依据。** 作废后样品不再参与 `LatestResult`，`Confirm` 对它返回 `water.ErrVoided`；但测量值、逐项所用上限与生效时间、逐项结论、整份超标标记和作废原因都保留在 `ListByPoint` 的记录里，仅供核对历史。
- **区分“历史记录”与“当前有效结果”要靠状态。** 作废记录的 `Exceeded`/`Results` 是作废前保存的快照，不是当前结论；`LatestResult` 只在 `StatusConfirmed` 的样品中按采样时间从晚到早选取，因此较晚样品作废后退到较早的已确认样品，且返回内容整体属于被选中的那份，不会把两份样品的测量或结论拼在一起。
- **没有有效样品时明确无结果。** `LatestResult` 返回空样品、`ok == false`、`err == nil`；待判定样品作废前后都没有判定依据，作废不会补出结论，其为假的超标标记和空返回都不能解释成达标。这与按点列表仍列得出作废记录互不矛盾——列表是台账，最近结果只看有效资格。
- **失败先处理错误。** 空白原因（`water.ErrEmptyField`）、对作废样品再确认（`water.ErrVoided`）等失败都返回空样品，不能当成功打印或继续使用；作废成功后原因随每次成功变更落盘，`Close` 后重新 `Open` 同一目录，作废状态、原因与历史依据仍可查。

## 打开时的记录完整性校验

本地文件可能被外部改动，只有记录完整可信时才应读入。`Open` 读入数据文件时会先核对每份样品的**原测量**，再核对每份**带结论**的样品（已确认样品，以及已确认后作废、逐项依据仍保留的样品）；正常流程保存的文件始终满足这些条件，校验只拦文件被外部改动或残缺后产生的异常记录。

**每份样品的原测量中，同一个测量项目名只能出现一次。** 这条规则对任何状态的样品都生效，包括没有逐项判定的**待判定样品**，以及待判定后直接作废、**没有历史判定依据**的样品——它们没有逐项结果本身不是损坏，但原测量重复仍会被挡在打开时，与正常录入时拒绝同一份样品重复项目的规则一致，不能等样品进入台账、甚至确认出结论后才发现：

- 重复按文件中保存的**完整项目名**判断，不依赖两条记录是否相邻，也不因数值相同而放行。例如同一份样品里的两条 pH 都是零，仍然是重复；pH 与浊度都测得零，则是两个不同项目，正常读入。同一项目出现在**不同样品**中是正常记录。
- 不会自行选择第一条或最后一条测量、合并相同值或删掉重复项；重复测量的样品无法被确认，也不会被补出判定。

带结论的样品还要满足原测量项目与逐项判定的对应规则。对应按**项目名称**匹配，与测量列表和判定列表的排列顺序无关；成功读入后两个列表仍各自保留原有顺序：

- 每份已确认样品**至少有一个测量项目**；一个测量项目都没有即记录有误。
- 每个测量项目**必须且只能**对应一条判定记录；判定中也**不能出现**原测量里没有的项目。
- 判定中同一项目**出现两次**视为记录有误。
- 对应项目的**测量值必须与原记录一致**，不能接受项目名称相同但测量值属于另一份记录的判定。数值**零是合法测量值**，按“有没有这个项目”判断缺项，不会把零当成没有填写。

例如一份已确认样品记录了 pH 和浊度两个测量项目，但只剩 pH 的判定依据，打开时就应失败——不能只凭整份超标标记返回达标或超标；如果两条判定都在，却把浊度的原测量值 `4` 写成了 `3`，同样拒绝。

发现任一这类问题时：

- **整次 `Open` 返回错误**（可被 `errors.Is(err, water.ErrCorruptRecord)` 识别），**不返回可用的数据存放对象**（`*Store` 为 `nil`），错误信息说明出问题的**样品编号和项目**；没有测量项目时也指出是哪份样品。
- **不静默跳过**有问题的样品后继续打开——同一文件里其他样品再正常也不行；**不选择第一条或最后一条测量、不合并相同值、不删掉重复项、不补出判定、不改成待判定、不覆盖原文件**。调用方据此明确知道原数据无法完整读取，需要先处理数据文件。

没有重复项目的正常记录继续按现有行为读取：已保存的上限、生效时间、逐项结论和整份超标标记都保留；后来新增或补录的限值即使会改变“现在重新判定”的结果，也**不会**据此拒绝已有完整依据，更不会在打开时重算结论。待判定样品没有判定记录、待判定后作废的样品没有历史依据，本身都不是损坏，不会被要求已经有判定依据；已确认后作废但历史依据完整的记录及现有查询行为（按点列表保留、`LatestResult` 跳过、再确认返回 `water.ErrVoided`）保持兼容。本次收紧只影响损坏记录的读取，不改变正常样品的录入与查询行为。
