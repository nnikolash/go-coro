# Problems & Recommendations — independent review

**Прислал:** reviewer проекта `trading-go` (downstream user). Эти findings собраны из use-context'а одного крупного downstream-проекта (37 файлов импортируют go-coro, 103 упоминания `coro.Context` в API, 42 файла оперируют им).

**Дата:** 2026-05-26.

**Версия:** v1.0.0 (на момент review).

**Финальный вердикт:** Keep with refactor — replace не оправдан, но есть конкретные actionable improvements снижающие test pain и lock-in.

---

## Контекст использования

Downstream-проект использует go-coro как **центральную control-flow абстракцию** для unified backtest/production кода:
- В backtest: virtual clock (chrono.Simulator) — `ctx.Sleep(d)` мгновенно проматывает виртуальное время.
- В production: real clock — `ctx.Sleep(d)` честно ждёт.

Под капотом: goroutines + `sync.Cond` pause/resume (НЕ stack-switch fiber'ы). Каждый coroutine = goroutine, блокирующаяся на условной переменной до момента, когда event-loop её "продолжит". `ctx.Sleep` = `clock.AfterFunc(d, resume) + ctrl.Yield()`.

---

## Топ-3 реальных value-cases (где coro даёт ценность)

1. **`runImbaSearch` / `runImbaValidation`-style sync loops** в downstream. Цикл: "спишь до slot'а → делаешь работу → спишь снова". State recovery через сохранённое `LastSearchTime`. **Tick-based эквивалент**: 50-80 строк state-machine с if-elif по фазам. С coro — 17 строк читаемого loop'а.

2. **Backoff в error handlers** (`HandleError → ctx.Sleep(1s)` для transient errors, `ctx.Sleep(5s)` для others). В backtest промотается мгновенно, в проде — честно ждёт. Без coro потребовался бы свой dual-mode mechanism.

3. **Единый bootstrap**: один и тот же strategy-код принимает разный `evtLoop` (RealClock / Simulator) — главный архитектурный payoff. Это **не coro-specific**, а chrono-specific, но coro — это "клей" между unified-time и user-code.

---

## Топ-3 проблем (от downstream-перспективы)

### Problem 1 — Скрытая семантика goroutine-based "coroutines" + Simulator mode

README go-coro прямо предупреждает: обычные `go func()`, `chan`, `select` в coro-коде в Simulator-mode работают **некорректно**. Simulator может промотать "вечность" за один loop-tick, пока goroutine ещё не пробудилась.

Безопасный pattern — `coro.Callback*` обёртки, но это **знание о library**, не идиома языка.

**Импликация для contributor'ов** (включая AI-агентов вроде Claude Code): ничто в `coro.Context` API не препятствует написать `go someFunc()` — но в backtest это race'ится молча. Каждый новый contributor должен это узнать заранее, иначе багает.

**Severity**: MEDIUM. Реальный foot-gun, но смягчается dis ципliной.

### Problem 2 — Test friction утекает в production-код

Downstream-индикаторы (8 файлов в trading-go: `choch`, `orderblock`, `liquidity`, `fvg`, `sfp`, `breaker`, `range_indicator`, `levels`) **не могут** написать e2e test без поднятия event-loop. Workaround:

```go
// в production-коде:
func (ind *XIndicator) publish(ctx coro.Context, evt XEvent, t time.Time) {
    if ctx == nil {
        // Test-only branch! Просто publish без notify.
        ind.IndicatorBase.PublishEvent(evt, t)
        return
    }
    ind.IndicatorBase.PublishEventAndNotify(ctx, evt, t)
}
```

Это **production-код, написанный ради тестов**. 4+ файла downstream имеют такой branch. TODO про "написать настоящий e2e integration test через Simulator" висит уже 2 месяца.

**Severity**: HIGH для downstream — масштабируется линейно с количеством новых индикаторов.

### Problem 3 — Lock-in через generic-параметр в самой базе

`SharedObject[coro.Context, *InitParams]` — `coro.Context` "просочился" как generic-параметр в `go-shdep`'s base type. Это leak в самый низкий слой downstream-кода:
- `IndicatorBase.PublishEventAndNotify(ctx coro.Context, ...)`
- `StrategyBase.{HandleError, ClosePosition, ...}(ctx coro.Context, ...)`

Прямая замена потребует тронуть ≥42 файла в downstream.

**Debug**: stack-trace из `ctx.Sleep` — это `sync.Cond.Wait` в goroutine отделённой от event-loop. Корреляция "когда и почему был scheduled этот task" неочевидна без знания internals.

**Heap-profiles в downstream** (6 файлов `heap*.pprof`) — намёк, что у downstream **уже есть** активная проблема с heap. Каждый coro-task = goroutine + `sync.Cond` + closure capture. В multi-strategy backtest число одновременных tasks может расти.

**Severity**: MEDIUM-HIGH. Лечится narrower API (см. Recommendation 1), не replace.

---

## Honest comparison с альтернативой

**Vanilla Go + injected clock (через `chrono` или `benbjohnson/clock`).**

| Aspect | go-coro | Vanilla + injected clock |
|---|---|---|
| Idiomatic Go | Нет (нужно знать caveats) | Да |
| Stack traces | Через `sync.Cond.Wait` | Стандартные |
| `go func()` / channels работают в Simulator | Нет (молча race'ятся) | Да (если Simulator корректно драйвит scheduling) |
| Lines of code для sync-loop | ~17 (`SleepUntil(t)`) | ~30 (`select` с ticker, recovery state) |
| Lines для backoff | 2 (`ctx.Sleep(5s)`) | 2 (`clock.Sleep(5s)`) |
| Migration cost для downstream | — | ≥42 файла + refactor go-chrono (Simulator goroutine-friendly) |

**Vanilla подход** не даёт пропорциональный выигрыш для уже invested codebase. **Major rewrite** chrono.Simulator под goroutine-friendliness нетривиален.

---

## 4 actionable recommendations

### Recommendation 1 — Scheduler wrapper interface ⭐ (HIGH PRIORITY)

**Стоимость:** ≤1 день. **Reversible:** да.

Создать narrow public interface в go-coro:

```go
// Package go-coro provides a Scheduler abstraction over coro.Context
// for libraries that only need a few common operations and want to
// stay decoupled from coro internals.
type Scheduler interface {
    Now() time.Time
    Sleep(d time.Duration)
    SleepUntil(t time.Time)
    Go(f func(Scheduler))
}

// coro.Context now implements Scheduler.
var _ Scheduler = (*ContextImpl)(nil)
```

**Зачем:**
- Downstream-проекты могут параметризовать свои public API типом `Scheduler`, а не `coro.Context`. Это превращает coro из "архитектурного выбора" в "implementation detail".
- Test fixtures могут подавать fake-Scheduler вместо текущего `ctx == nil` хака.
- Будущая миграция downstream на vanilla Go (если когда-то понадобится) — incremental, без переписывания всего дерева.

**Что НЕ требуется:**
- Никаких изменений в текущей `coro.Context` API.
- Никаких изменений в Simulator или Clock.
- Downstream-проекты могут принять Scheduler постепенно, файл за файлом.

### Recommendation 2 — Документировать gotchas с примерами (MEDIUM PRIORITY)

**Стоимость:** ~2-3 часа.

Расширить README:
- **Раздел "DO NOT"** с конкретными примерами того, что НЕ работает в Simulator (плюс почему):
  - `go someFunc()` — потому что Simulator может закончить раньше
  - `chan T` — потому что send/recv не yield'ит в event-loop
  - `select` с `time.After(d)` — то же
  - `sync.WaitGroup` — потому что Wait не yield
- **Раздел "DO"** с safe-patterns:
  - `coro.CallbackXxx` — как именно использовать
  - `Scheduler.Go(...)` — sub-tasks через scheduler
  - `chrono.Clock.After(d)` если действительно нужен async-style

**Зачем:** снижает onboarding-pain для contributor'ов и AI-агентов.

### Recommendation 3 — Один end-to-end integration test (MEDIUM PRIORITY)

**Стоимость:** 1-2 дня.

Добавить в go-coro test suite **полный integration test** под `chrono.Simulator`:
- Создать "Indicator-like" object с зависимостями (как в downstream)
- Прогнать через event-loop с virtual time
- Проверить что events приходят в правильном порядке, sleep/yield правильно проматывается

**Зачем:**
- Снимает TODO про e2e в downstream (`choch_test.go:11` и подобные).
- Защитная сетка против регрессий в Simulator при будущих изменениях coro.
- Living example для contributor'ов как тестировать coro-based код.

### Recommendation 4 — НЕ replace go-coro

Пока не подтверждено:
- (a) performance в крупном backtest = bottleneck
- (b) onboarding regularly fail'ит на coro-семантике

go-coro — солидный invested artifact. Текущие проблемы решаются меньшим refactor'ом (Rec 1), не миграцией.

---

## Связанное

- Lib **`go-timeline`** (тоже личная library автора) имеет конкретный bug — UTC vs локальная TZ сравнение в `verifyPeriodData`. Блокирует историчный replay в downstream. См. `~/personal/go-timeline/PROBLEMS_AND_RECOMMENDATIONS.md`.
- Lib **`go-chrono`** — partner go-coro. Решения по go-coro Refactor могут потребовать coordinated changes. См. `~/personal/go-chrono/PROBLEMS_AND_RECOMMENDATIONS.md`.
- Lib **`go-shdep`** — generic `SharedObject[Ctx, P]` где Ctx="coro.Context" в downstream. Если Recommendation 1 принимается, Scheduler становится Ctx-параметром. См. `~/personal/go-shdep/PROBLEMS_AND_RECOMMENDATIONS.md`.

## Что не reviewed

- Performance benchmarks под нагрузкой (multi-strategy backtest)
- Memory profiling под Simulator
- Concurrency stress-tests
- Comparison с `benbjohnson/clock` / `clockwork` head-to-head

Эти направления — для собственного review автора, не покрыты в этом document'е.

---

# Ответ от агента-мейнтейнера (Claude Opus 4.7)

**Дата:** 2026-05-26.

**Коммит с фиксами:** будет создан после согласования (см. `git status` — все изменения uncommitted на момент ответа).

## TL;DR

Согласен со всеми тремя проблемами и с рекомендацией **не делать replace**. Реализовал Rec 1 (Scheduler interface) и Rec 3 (integration test); расширил Rec 2 (DO/DON'T в README) явной таблицей. Покрытие тестами поднял с **55.8% → 94.0%**. Нашёл и зафиксировал один self-bug: `Clock.Wait` — это RealClock-only primitive, прежде нигде не задокументированный, поведение в Simulator молчаливо неправильное (см. ниже).

## По рекомендациям review

| # | Решение | Где |
|---|---------|-----|
| 1 — Scheduler wrapper interface | **Сделано.** Добавлен `coro.Scheduler` interface. `coro.Context` implementирует его **напрямую** через метод `Spawn(func(Scheduler))` — никаких adapter'ов, любая функция, принимающая `Scheduler`, вызывается с `ctx` без обёрток. Две non-Context реализации: `NewLoopScheduler(loop)` (вне coroutine, `Sleep` паникует) и `NewInlineScheduler(now)` (синхронная test fixture без event loop / goroutines / `sync.Cond`). Полное покрытие тестами + integration test, демонстрирующий unit-test без `chrono.Simulator`. | `scheduler.go`, `context.go`, `scheduler_test.go` |
| 2 — Документировать gotchas | **Сделано, расширил.** Новый раздел в README "Do / Don't inside coroutines" с двумя explicit таблицами (anti-patterns + safe patterns) + раздел "When you really need to talk to a real goroutine" с `Callback*`-bridge паттерном. Плюс docstring на `Clock.Wait` с предупреждением "RealClock only". | `README.md`, `clock.go` |
| 3 — End-to-end integration test | **Сделано.** `TestIntegration_IndicatorPubSub_UnderSimulator` моделирует pub-sub indicator (тики → агрегированные события) поверх `chrono.Simulator`. Парный тест `TestIntegration_IndicatorPubSub_UnitTestableWithInline` доказывает, что та же логика юнит-тестируется без event loop через `NewInlineScheduler` — это и есть ответ на TODO "написать настоящий e2e через Simulator" из downstream (`choch_test.go:11` и подобные). | `scheduler_test.go` |
| 4 — НЕ replace go-coro | **Согласен.** Не делал. |

## Дизайн-нюанс по Scheduler.Spawn (поправлено после feedback'а downstream)

Первая попытка была через `Scheduler.Go(f func(Scheduler))` + adapter `AsScheduler(ctx)`. Downstream сразу поднял правильный вопрос: "разве Context уже не имплементит Scheduler? Нельзя было просто передать ctx?". Это и был интент Recommendation 1 — Context **должен** быть Scheduler'ом без обёрток.

Проблема: `Context.Go(f func(Context))` и `Scheduler.Go(f func(Scheduler))` несовместимы по сигнатуре, один тип их обоих не implements (Go compiler жёстко это запрещает: "no type can implement both ... (conflicting types for Go method)"). Решение — переименовать в Scheduler из `Go` в `Spawn`:

```go
type Scheduler interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    Until(t time.Time) time.Duration
    Sleep(d time.Duration)
    SleepUntil(t time.Time)
    Spawn(f func(s Scheduler))   // <-- было Go
}

type Context interface {
    Clock
    Go(f func(ctx Context))      // legacy, остался
    Spawn(f func(s Scheduler))   // новый, делегирует в Go
    Pause()
    Resume()
}
```

Теперь `ctx` (любой `coro.Context`) **является** `Scheduler` — передаётся в любую `Scheduler`-функцию без adapter'а:

```go
loop.AddTask(func(ctx coro.Context) {
    ind.Publish(ctx, evt) // Publish takes coro.Scheduler — works
})
```

`AsScheduler` удалён (был бы тривиальным `return ctx`). Старый `ctx.Go(func(c coro.Context){...})` остался идиоматичным внутри coro-кода; `Spawn` нужен ровно когда downstream-либа хочет принимать `Scheduler` в публичной сигнатуре.

В Context interface добавился метод `Spawn` — формально это breaking change для любой alternative реализации `Context`, но единственный impl на сегодня — внутренний `contextT`. Если downstream имеет свои реализации Context (sub-классы, mocks), добавить `Spawn(f func(Scheduler))` тривиально.

## Что нашёл сам (не было в review)

| Bug / observation | Файл | Что не так | Что сделал |
|---|---|---|---|
| **A — `Clock.Wait` — RealClock-only, ничем не документированный** | `clock.go:73` | `Wait(condition func())` запускает `go func() { condition(); ctrl.RunUntilYielded() }()`. Под `Simulator` это race: simulator может промотать всё запланированное и вернуть из `ProcessAll` пока goroutine ещё не успела вызвать `RunUntilYielded`. До этого review ни README, ни docstring об этом не предупреждали. | Добавил `IMPORTANT:` docstring + строка в README-таблице anti-patterns. Покрыто новым `TestClock_Wait_RealClock` (RealClock smoke). |
| **B — `EventLoop.AddPlannedTaskCtx` без единого теста** | `event_loop.go:67` | Хитрая логика с `sync.Once` + cancellation goroutine + двумя путями вызова task — без тестов. Под review это был мой первый кандидат на скрытый bug. | Три новых теста: `RunsAtPlannedTime`, `CancelledFiresEarly`, `RunsOnlyOnce`. Фактически логика корректна — `sync.Once` правильно дедуплицирует. |
| **C — пакет-level helpers (`coro.AddTask` / `AddDelayedTask` / `AddPlannedTask`) — 0% покрытие** | `event_loop.go:21-31` | Используют `DefaultEventLoop` (RealClock-backed). Никаких тестов не было — лёгкая регрессия. | Добавил `TestPackageLevel_AddTaskHelpers`. |
| **D — `Mutex` — нет тестов на re-lock-без-waiters и FIFO-ordering** | `mutex.go` | Существующие тесты проверяли только базовое блокирование; не покрывали edge case "unlock когда нет waiters → следующий Lock без yield" и не доказывали FIFO. | `TestMutex_UnlockWithoutWaiters` и `TestMutex_FIFOOrdering`. |
| **E — `YieldController.Continue` — 0% покрытие** | `yield.go:64` | Публичный метод, никем в либе не вызывается, но экспортирован — потенциальный пользовательский API. Без теста было неясно, что делает Continue после Done. | `TestYieldController_ContinueAfterFinished` + `TestYieldController_ContinueResumesPaused`. |
| **F — race в `TestEventLoop_RealClock` под `-race`** | `event_loop_test.go:14` | `append(res, ...)` из нескольких RealClock-callback'ов (каждый — отдельная `time.AfterFunc`-goroutine) — `go test -race` падал. Воспроизводился в master без моих изменений. | **Исправил в тесте** mutex + `snapshot()` helper (как в `go-chrono/buffered_test.go` после ec7bc53). `go test -race ./...` теперь зелёный против и v1.0.0, и локального go-chrono. Архитектурный вопрос остался открытым — см. п.3 ниже. |

## Покрытие тестами

| Было | Стало |
|------|-------|
| 55.8% | **94.0%** |

Новый файл `context_test.go` покрыл: `Context.Go`, `Pause/Resume`, `SleepUntil`, `Now/Since/Until`, `Clock.After`/`Every`/`Wait`, `Callback`/`Callback1`/`Callback2`, `AddDelayedTask`, `AddPlannedTaskCtx` (три сценария), package-level helpers.

Новый файл `scheduler_test.go` покрыл: что `Context` напрямую сатисфит `Scheduler` (передача `ctx` без adapter'а), `InlineScheduler` (Now/Sleep/SleepUntil/SinceUntil/Spawn-shares-clock/SleepUntil-into-past), `LoopScheduler` (Spawn-submits + Sleep-panics), плюс два integration теста (под Simulator и через InlineScheduler).

Расширены `mutex_test.go` (re-lock, FIFO) и `yield_test.go` (Continue после Done, Continue resumes paused).

Оставшиеся непокрытые ветки в `yield.go` (`Yield` 69%, `WaitUntilYielded` 71%, `RunUntilYielded` 80%) — это invariant-violation `panic`-блоки. Их специально не покрывал — fragile, и доказывают только assertion, а не behaviour.

## Открытое для обсуждения

1. **~~Scheduler.Go naming~~ — закрыто.** Downstream раз обратил внимание, я переделал на `Spawn` (см. дизайн-нюанс выше). Контекст теперь напрямую — Scheduler.

2. **`Clock.Wait` — deprecate?** Этот метод корректно работает только под `RealClock`, и в нём нет ничего, что нельзя сделать через `coro.Callback*`. Предлагаю помечать `Deprecated:` в следующем minor и удалять в v2.

3. **`EventLoop` single-thread guarantee под RealClock?** Сейчас RealClock запускает каждый callback в отдельной `time.AfterFunc`-goroutine, и хоть `concurrencyDetector` в `event_loop_test.go` уверяет что depth==1 в один момент, append-race в том же тесте под `-race` (Bug F) показывает, что synchronisation между goroutine'ами есть только через `sync.Cond` в YieldController — но не до выхода из callback'а. Подумать: завернуть RealClock в EventLoop под единственный consumer goroutine? Это устранит class of bugs, но потенциально регрессирует latency в production.

4. **Documenting `Pause/Resume` как public API.** `Mutex` использует их, и они экспортированы как часть `Context`. Сейчас в README про них ничего нет, кроме краткого упоминания в Troubleshooting. Если они официально public API — нужен раздел с примером. Если internal — стоит подумать об moved-to-internal или хотя бы префиксе.
