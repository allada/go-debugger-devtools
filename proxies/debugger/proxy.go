package debugger

import (
    "fmt"
    "io/ioutil"
    "strconv"
    "sync"
    "strings"
    "crypto/sha1"
    "../../dbgClient"
    "../../protocol/shared"
    debuggerAgent "../../protocol/debugger"
    runtimeAgent "../../protocol/runtime"
    targetAgent "../../protocol/target"
)

type goroutineID int

type Target struct {
    ID goroutineID
    Proxy *proxy
}

func (t *Target) Attach(routine dbgClient.Goroutine) {
    var name string
    if routine.UserCurrentLoc.Function != nil {
        parts := strings.Split(routine.UserCurrentLoc.Function.Name, ".")
        name = " " + parts[len(parts) - 1]
    }
    t.Proxy.target.FireAttachedToTarget(targetAgent.AttachedToTargetEvent{
        TargetInfo: targetAgent.TargetInfo{
            TargetId: targetAgent.TargetID(fmt.Sprintf("%d", t.ID)),
            Type: "node",
            Title: "tt",
            Url: fmt.Sprintf("%d:%s", t.ID, name),
        },
        WaitingForDebugger: false,
    })

    for _, file := range t.Proxy.fileList {
        t.Proxy.agent.FireScriptParsedOnTarget(fmt.Sprintf("%d", t.ID), debuggerAgent.ScriptParsedEvent{
            ScriptId: runtimeAgent.ScriptId(file),
            Url: file,
            ExecutionContextId: 1,
        })
    }
}

func (t *Target) Destroy() {
    t.Proxy.target.FireDetachedFromTarget(targetAgent.DetachedFromTargetEvent{
        TargetId: targetAgent.TargetID(fmt.Sprintf("%d", t.ID)),
    })
}

func (t *Target) FireResumed() {
    t.Proxy.agent.FireResumedOnTarget(fmt.Sprintf("%d", t.ID))
}

func (t *Target) FirePaused(callframes []dbgClient.Stackframe) {
    sendFrames := []debuggerAgent.CallFrame{}
    for index, frame := range callframes {
        functionName := "<Unknown>"
        if frame.Location.Function != nil {
            functionName = frame.Location.Function.Name
        }
        sendFrames = append(sendFrames, debuggerAgent.CallFrame{
            CallFrameId: debuggerAgent.CallFrameId(fmt.Sprintf("%d", index)),
            FunctionName: functionName,
            Location: debuggerAgent.Location{
                ScriptId: runtimeAgent.ScriptId(frame.Location.File),
                LineNumber: int64(frame.Location.Line - 1), // Always -1
            },
            ScopeChain: t.Proxy.buildScopeChain(index),
            This: runtimeAgent.RemoteObject{
                Type: "undefined",
            },
            ReturnValue: nil,
        })
    }
    t.Proxy.agent.FirePausedOnTarget(fmt.Sprintf("%d", t.ID), debuggerAgent.PausedEvent{
        Reason: "other",
        CallFrames: sendFrames,
    })
}

type runtimer interface{
    CreateContext()
    MakeRemoteObject(dbgClient.Variable) runtimeAgent.RemoteObject
}

type proxy struct {
    agent *debuggerAgent.DebuggerAgent
    target *targetAgent.TargetAgent
    client *dbgClient.Client
    runtime runtimer

    activeTargetsMux sync.RWMutex
    activeTargets map[goroutineID]*Target
    fileList []string
    activeGoroutineID goroutineID
    breakpointsMux sync.Mutex
    breakpoints map[string]struct{}
}

func NewProxy(conn *shared.Connection, client *dbgClient.Client) *proxy {
    agent := debuggerAgent.NewAgent(conn)
    target := targetAgent.NewAgent(conn)
    return &proxy{
        agent: agent,
        target: target,
        client: client,
        activeTargets: map[goroutineID]*Target{},
        breakpoints: map[string]struct{}{},
    }
}

func (p *proxy) Start(runtime runtimer) {
    p.runtime = runtime
    // Wait until we are enabled.
    command := <-p.agent.EnableNotify()
    command.Respond()

    go p.handleNotifications()
    go p.runtime.CreateContext()

    // Wait until debugger is ready.
    p.client.BlockUntilReady()

    state, err := p.client.GetState()
    if err != nil {
        panic(err)
    }

    if state.SelectedGoroutine != nil {
        p.activeGoroutineID = goroutineID(state.SelectedGoroutine.ID)
    }

    go p.sendPauseState()

    sources, err := p.client.ListSources()
    if err != nil {
        panic(err)
    }

    for _, source := range sources {
        if source == "<autogenerated>" {
            continue;
        }
        p.fileList = append(p.fileList, source)
        p.agent.FireScriptParsed(debuggerAgent.ScriptParsedEvent{
            ScriptId: runtimeAgent.ScriptId(source),
            Url: source,
            ExecutionContextId: 1,
        })
    }
}

func (p *proxy) handleNotifications() {
    enable                  := p.agent.EnableNotify()
    disable                 := p.agent.DisableNotify()
    setBreakpointsActive    := p.agent.SetBreakpointsActiveNotify()
    setSkipAllPauses        := p.agent.SetSkipAllPausesNotify()
    setBreakpointByUrl      := p.agent.SetBreakpointByUrlNotify()
    setBreakpoint           := p.agent.SetBreakpointNotify()
    removeBreakpoint        := p.agent.RemoveBreakpointNotify()
    getPossibleBreakpoints  := p.agent.GetPossibleBreakpointsNotify()
    continueToLocation      := p.agent.ContinueToLocationNotify()
    stepOver                := p.agent.StepOverNotify()
    stepInto                := p.agent.StepIntoNotify()
    stepOut                 := p.agent.StepOutNotify()
    pause                   := p.agent.PauseNotify()
    resume                  := p.agent.ResumeNotify()
    searchInContent         := p.agent.SearchInContentNotify()
    setScriptSource         := p.agent.SetScriptSourceNotify()
    restartFrame            := p.agent.RestartFrameNotify()
    getScriptSource         := p.agent.GetScriptSourceNotify()
    setPauseOnExceptions    := p.agent.SetPauseOnExceptionsNotify()
    evaluateOnCallFrame     := p.agent.EvaluateOnCallFrameNotify()
    setVariableValue        := p.agent.SetVariableValueNotify()
    setAsyncCallStackDepth  := p.agent.SetAsyncCallStackDepthNotify()
    setBlackboxPatterns     := p.agent.SetBlackboxPatternsNotify()
    setBlackboxedRanges     := p.agent.SetBlackboxedRangesNotify()

    // TODO bail out properly on closed.
    for {
        select {
        case command := <-enable:
            command.Respond()
        case command := <-disable:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setBreakpointsActive:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setSkipAllPauses:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setBreakpointByUrl:
            go p.setBreakpointAndRespond(command)
        case command := <-setBreakpoint:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-removeBreakpoint:
            go p.removeBreakpointAndRespond(command)
        case command := <-getPossibleBreakpoints:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-continueToLocation:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-stepOver:
            go p.stepOverAndRespond(command)
        case command := <-stepInto:
            go p.stepIntoAndRespond(command)
        case command := <-stepOut:
            go p.stepOutAndRespond(command)
        case command := <-pause:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-resume:
            go p.continueAndRespond(command)
        case command := <-searchInContent:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setScriptSource:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-restartFrame:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-getScriptSource:
            // TODO this should be secure
            go getFileAndRespond(command)
        case command := <-setPauseOnExceptions:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-evaluateOnCallFrame:
            go p.evaluateOnGoroutineAndRespond(command)
        case command := <-setVariableValue:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setAsyncCallStackDepth:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setBlackboxPatterns:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        case command := <-setBlackboxedRanges:
            command.RespondWithError(shared.ErrorCodeMethodNotFound, "")
        }
    }
}

func buildLocation(file string, line int) debuggerAgent.Location {
    return debuggerAgent.Location{
        ScriptId: runtimeAgent.ScriptId(file),
        LineNumber: int64(line),
    }
}

func (p *proxy) stepOverAndRespond(command debuggerAgent.StepOverCommand) {
    if command.DestinationTargetID != "" {
        if targetID, err := strconv.Atoi(command.DestinationTargetID); err == nil {
            p.activeGoroutineID = goroutineID(targetID)
        } else {
            command.RespondWithError(shared.ErrorCodeInternalError, "Could not convert targetID to int")
            panic(err)
        }
    }
    
    _, err := p.client.SwitchGoroutine(int(p.activeGoroutineID))
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
    }

    p.sendResumeState()
    _, err = p.client.Next()

    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
    }
    command.Respond()
    p.sendPauseState()
}

func (p *proxy) stepIntoAndRespond(command debuggerAgent.StepIntoCommand) {
    if command.DestinationTargetID != "" {
        if targetID, err := strconv.Atoi(command.DestinationTargetID); err == nil {
            p.activeGoroutineID = goroutineID(targetID)
        } else {
            command.RespondWithError(shared.ErrorCodeInternalError, "Could not convert targetID to int")
            panic(err)
        }
    }
    
    _, err := p.client.SwitchGoroutine(int(p.activeGoroutineID))
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
        return
    }

    p.sendResumeState()
    _, err = p.client.Step()
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
        return
    }
    command.Respond()
    p.sendPauseState()
}

func (p *proxy) stepOutAndRespond(command debuggerAgent.StepOutCommand) {
    if command.DestinationTargetID != "" {
        if targetID, err := strconv.Atoi(command.DestinationTargetID); err == nil {
            p.activeGoroutineID = goroutineID(targetID)
        } else {
            command.RespondWithError(shared.ErrorCodeInternalError, "Could not convert targetID to int")
            panic(err)
        }
    }
    
    _, err := p.client.SwitchGoroutine(int(p.activeGoroutineID))
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
        return
    }

    p.sendResumeState()
    _, err = p.client.StepOut()
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
        return
    }
    command.Respond()
    p.sendPauseState()
}

func (p *proxy) continueAndRespond(command debuggerAgent.ResumeCommand) {
    if command.DestinationTargetID != "" {
        if targetID, err := strconv.Atoi(command.DestinationTargetID); err == nil {
            p.activeGoroutineID = goroutineID(targetID)
        } else {
            command.RespondWithError(shared.ErrorCodeInternalError, "Could not convert targetID to int")
            panic(err)
        }
    }
    
    _, err := p.client.SwitchGoroutine(int(p.activeGoroutineID))
    if err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        panic(err)
    }
    command.Respond()

    p.sendResumeState()
    state, ok := <-p.client.Continue()

    if !ok {
        panic("It appears program has exited");
        return
    }
    if state != nil && state.SelectedGoroutine != nil {
        p.activeGoroutineID = goroutineID(state.SelectedGoroutine.ID)
    }
    p.sendPauseState()
}


func (p *proxy) sendResumeState() {
    p.activeTargetsMux.RLock()
    defer p.activeTargetsMux.RUnlock()
    for _, target := range p.activeTargets {
        target.FireResumed()
    }
    p.agent.FireResumed()
}

func (p *proxy) sendPauseState() {
    state, err := p.client.GetState()
    if err != nil {
        panic(err)
    }
    p.syncGoroutines()
    // TODO Need some checks here on state.
    if state == nil {
        panic("Called sendPauseState() but not paused.")
    }

    p.activeTargetsMux.RLock()

    var activeStack *[]dbgClient.Stackframe
    targetsStacks := map[*Target][]dbgClient.Stackframe{}
    for routineID, target := range p.activeTargets {
        stacks, err := p.client.Stacktrace(int(routineID), 50, &dbgClient.LoadConfig{
            FollowPointers: true,
            MaxVariableRecurse: 1,
            MaxStringLen: 1,
            MaxArrayValues: 1,
            MaxStructFields: 1,
        })
        if err != nil {
            // TODO Something better here.
            panic(err)
        }
        if routineID == p.activeGoroutineID {
            activeStack = &stacks
            continue
        }
        targetsStacks[target] = stacks
    }

    p.activeTargetsMux.RUnlock()

    if activeStack != nil {
        // TODO move this code.
        sendFrames := []debuggerAgent.CallFrame{}
        for index, frame := range *activeStack {
            functionName := "<Unknown>"
            if frame.Location.Function != nil {
                functionName = frame.Location.Function.Name
            }
            sendFrames = append(sendFrames, debuggerAgent.CallFrame{
                CallFrameId: debuggerAgent.CallFrameId(fmt.Sprintf("%d", index)),
                FunctionName: functionName,
                Location: debuggerAgent.Location{
                    ScriptId: runtimeAgent.ScriptId(frame.Location.File),
                    LineNumber: int64(frame.Location.Line - 1), // Always -1
                },
                ScopeChain: p.buildScopeChain(index),
                This: runtimeAgent.RemoteObject{
                    Type: "undefined",
                },
                ReturnValue: nil,
            })
        }
        p.agent.FirePaused(debuggerAgent.PausedEvent{
            Reason: "other",
            CallFrames: sendFrames,
        })
    }

    for target, stacks := range targetsStacks {
        target.FirePaused(stacks)
    }
}

func (p *proxy) buildScopeChain(frameId int) []debuggerAgent.Scope {
    objectId := runtimeAgent.RemoteObjectId(fmt.Sprintf("local:%d", frameId))
    return []debuggerAgent.Scope{
        debuggerAgent.Scope{
            Type: debuggerAgent.ScopeTypeLocal,
            Object: runtimeAgent.RemoteObject{
                Type: runtimeAgent.RemoteObjectTypeObject,
                ObjectId: &objectId,
            },
        },
    }
}

func (p *proxy) syncGoroutines() {
    p.activeTargetsMux.Lock()
    defer p.activeTargetsMux.Unlock()
    routines, err := p.client.ListGoroutines()
    if err != nil {
        panic(err)
    }
    foundTargets := map[goroutineID]struct{}{}
    for _, routine := range routines {
        id := goroutineID(routine.ID)
        foundTargets[id] = struct{}{}
        _, ok := p.activeTargets[id]
        if !ok {
            target := &Target{
                ID: id,
                Proxy: p,
            }
            target.Attach(*routine)
            p.activeTargets[id] = target
        }
    }
    for routineID, target := range p.activeTargets {
        if _, ok := foundTargets[routineID]; !ok {
            target.Destroy()
            delete(p.activeTargets, routineID)
        }
    }
    if _, ok := p.activeTargets[p.activeGoroutineID]; !ok {
        // Grab first item in activeTargets since one was not found.
        for index, _ := range p.activeTargets {
            p.activeGoroutineID = index
            break;
        }
    }
}

func (p *proxy) setBreakpointAndRespond(command debuggerAgent.SetBreakpointByUrlCommand) {
    if command.UrlRegex != nil {
        command.RespondWithError(shared.ErrorCodeInvalidParams, "urlRegex not available")
        return
    }
    if command.ColumnNumber != nil && *command.ColumnNumber != 0 {
        command.RespondWithError(shared.ErrorCodeInvalidParams, "columnNumber not available")
        return
    }
    if command.Condition != nil && *command.Condition != "" {
        command.RespondWithError(shared.ErrorCodeInvalidParams, "condition not available")
        return
    }
    if command.Url == nil {
        command.RespondWithError(shared.ErrorCodeInvalidParams, "url must be set")
        return
    }

    // Start with "a" because cannot start just be a number.
    breakpointKey := fmt.Sprintf("a%x", sha1.Sum([]byte(fmt.Sprintf("%s:%d", *command.Url, command.LineNumber))))
    p.breakpointsMux.Lock()
    defer p.breakpointsMux.Unlock()
    if _, ok := p.breakpoints[breakpointKey]; ok {
        // Breakpoint already set.
        p.sendBreakpointSet(command, debuggerAgent.BreakpointId(breakpointKey))
        return
    }
    // Always +1 from what devtools says.
    _, err := p.client.CreateBreakpointAtLine(*command.Url, int(command.LineNumber + 1), breakpointKey)
    if err != nil {
        delete(p.breakpoints, breakpointKey)
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        return
    }
    p.breakpoints[breakpointKey] = struct{}{}
    p.sendBreakpointSet(command, debuggerAgent.BreakpointId(breakpointKey))
}

func (p *proxy) sendBreakpointSet(command debuggerAgent.SetBreakpointByUrlCommand, breakpointId debuggerAgent.BreakpointId) {
    command.Respond(&debuggerAgent.SetBreakpointByUrlReturn{
        //BreakpointId: debuggerAgent.BreakpointId(fmt.Sprintf("%d", breakpoint.ID)),
        BreakpointId: breakpointId,
        Locations: []debuggerAgent.Location{
            buildLocation(*command.Url, int(command.LineNumber)),
        },
    })
}

func (p *proxy) removeBreakpointAndRespond(command debuggerAgent.RemoveBreakpointCommand) {
    breakpointId := string(command.BreakpointId)
    p.breakpointsMux.Lock()
    delete(p.breakpoints, breakpointId)
    p.breakpointsMux.Unlock()
    if err := p.client.ClearBreakpointByName(breakpointId); err != nil {
        command.RespondWithError(shared.ErrorCodeInternalError, err.Error())
        return
    }
    command.Respond()
}

func (p *proxy) evaluateOnGoroutineAndRespond(command debuggerAgent.EvaluateOnCallFrameCommand) {
    goroutineID := int(p.activeGoroutineID)
    if command.DestinationTargetID != "" {
        if targetID, err := strconv.Atoi(command.DestinationTargetID); err == nil {
            goroutineID = targetID
        } else {
            command.RespondWithError(shared.ErrorCodeInternalError, "Could not convert targetID to int")
            panic(err)
        }
    }
    frameId, err := strconv.Atoi(string(command.CallFrameId));
    if err != nil {
        panic(err)
    }
    variable, err := p.client.EvalVariable(dbgClient.EvalScope{
        GoroutineID: goroutineID,
        Frame: frameId,
    }, command.Expression, dbgClient.LoadConfig{
        FollowPointers: true,
        MaxVariableRecurse: 1,
        MaxStringLen: 500,
        MaxArrayValues: 1,
        MaxStructFields: 1,
    })
    if err != nil {
        fmt.Println("Error: " + err.Error())
        command.Respond(&debuggerAgent.EvaluateOnCallFrameReturn{
            ExceptionDetails: &runtimeAgent.ExceptionDetails{
                ExceptionId: 1,
                Text: err.Error(),
                LineNumber: -1,
                ColumnNumber: -1,
            },
        })
        return
    }
    command.Respond(&debuggerAgent.EvaluateOnCallFrameReturn{
        Result: p.runtime.MakeRemoteObject(*variable),
    })
}

func getFileAndRespond(command debuggerAgent.GetScriptSourceCommand) {
    data, err := ioutil.ReadFile(string(command.ScriptId))
    if err != nil {
        //fmt.Println(err)
        panic(err)
        return
    }
    command.Respond(&debuggerAgent.GetScriptSourceReturn{
        ScriptSource: string(data),
    })
}
