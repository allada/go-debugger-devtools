package systeminfo


import (
    "../shared"
    "sync"
    "encoding/json"
    "fmt"
)
type GetInfoCommandFn struct {
    mux sync.RWMutex
    fn func(GetInfoCommand)
}

func (a *GetInfoCommandFn) Load() func(GetInfoCommand) {
    a.mux.RLock()
    defer a.mux.RUnlock()
    return a.fn
}

func (a *GetInfoCommandFn) Store(fn func(GetInfoCommand)) {
    a.mux.Lock()
    defer a.mux.Unlock()
    a.fn = fn
}

type SystemInfoAgent struct {
    conn *shared.Connection
    commandHandlers struct {
        GetInfo GetInfoCommandFn
    }
}
func NewAgent(conn *shared.Connection) *SystemInfoAgent {
    agent := &SystemInfoAgent{
        conn: conn,
    }
    conn.RegisterAgent(agent)
    return agent
}

func (agent *SystemInfoAgent) Name() string {
    return "SystemInfo"
}

func (agent *SystemInfoAgent) ProcessCommand(id int64, targetId string, funcName string, data *json.RawMessage) {
    defer func() {
        data := recover()
        switch data.(type) {
        case nil:
            return
        case shared.Warning:
            fmt.Println(data)
        case shared.Error:
            fmt.Println("Closing websocket because of following Error:")
            fmt.Println(data)
            agent.conn.Close()
        case error:
            fmt.Println("Closing websocket because of following Error:")
            fmt.Println(data)
            agent.conn.Close()
        default:
            fmt.Println("Should probably use shared.Warning or shared.Error instead to panic()")
            panic(data)
        }
    }()
    switch (funcName) {
        case "getInfo":
            var out GetInfoCommand
            agent.conn.SetupCommand(id, targetId, data, &out)
            fn := agent.commandHandlers.GetInfo.Load()
            if fn == nil {
                out.RespondWithError(shared.ErrorCodeMethodNotFound, "")
                break
            }
            fn(out)
        default:
            fmt.Printf("Command %s unknown\n", funcName)
            agent.conn.SendErrorResult(id, targetId, shared.ErrorCodeMethodNotFound, "")
    }
}

// Dispatchable Events

// Commands Sent From Frontend
func (agent *SystemInfoAgent) SetGetInfoHandler(handler func(GetInfoCommand)) {
    agent.commandHandlers.GetInfo.Store(handler)
}
func init() {

}