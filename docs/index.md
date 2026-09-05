## One pipeline, eight views

`generate`, `check`, `graph`, `explain`, `why`, `list`, `config` and `doctor` are
not eight tools. They are eight windows onto the same six stages, which run in the
same order every time — though only `generate` and `check` reach the last one.

```mermaid
flowchart LR
    L["load<br/>one type-checked<br/>go/packages session"]
    F["find spec<br/>roots, Bind, Override,<br/>Scoped, HTTP, Value,<br/>Include, ConfigFile"]
    S["scan<br/>every constructor-shaped<br/>function, classified"]
    RT["routes<br/>every //servo: directive,<br/>validated module-wide"]
    R["resolve<br/>closure → precedence →<br/>cycles → levels"]
    E["emit<br/>one deterministic,<br/>gofmt-clean file"]

    L --> F --> S --> RT --> R --> E
```

Loading happens once per module, however many injectors it contains, and so does
the route scan: a `//servo:get /healthz` comment declares an HTTP route wherever
it lives, and a spec declaring `servo.HTTP()` gets the servers for it emitted —
mux, typed request decoding, middleware, lifecycle. Everything else runs per
injector, because a monorepo's `cmd/api`, `cmd/worker` and `cmd/migrator` do not
share a graph even when they share a type-checking session.

Resolution has exactly two outcomes: a complete ordered plan, or a set of
diagnostics. Never a partial graph.

## What the generated app does when it runs

`New`, `Run` and `Shutdown` are the whole lifecycle. Start-up follows dependency
order and unwinds if any step fails; emitted HTTP servers are constructed last,
serve inside `Run`, and drain first. Shutdown runs the same list backwards under
a time budget, and reports anything that refused to stop rather than hanging on
it forever.

```mermaid
flowchart TD
    C["Construct<br/>constructors called in dependency order"]
    I["Init<br/>level by level — independent nodes concurrently"]
    OK{"every Init<br/>returned nil?"}
    RB["Roll back<br/>Shutdown(ctx), joined with the failing error"]
    RUN["Running"]
    S["Shutdown<br/>reverse dependency order"]
    B{"returned inside<br/>the stop budget?"}
    DONE["StatusOK / StatusFailed"]
    AB["StatusAbandoned<br/>reported, not waited on"]

    C --> I --> OK
    OK -- no --> RB
    OK -- yes --> RUN --> S --> B
    B -- yes --> DONE
    B -- no --> AB
```

None of that is a framework callback. It is ordinary Go, in a file you can open,
step through in a debugger, and review in a pull request like any other code.
