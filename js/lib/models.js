export var ActionType;
(function (ActionType) {
    ActionType["Gather"] = "gather";
    ActionType["Craft"] = "craft";
    ActionType["Hunt"] = "hunt";
    ActionType["Explore"] = "explore";
    ActionType["Build"] = "build";
    ActionType["Smelt"] = "smelt";
    ActionType["Farm"] = "farm";
    ActionType["Mine"] = "mine";
    ActionType["MarkLocation"] = "mark_location";
    ActionType["RecallLocation"] = "recall_location";
    ActionType["Idle"] = "idle";
    ActionType["Sleep"] = "sleep";
    ActionType["Retreat"] = "retreat";
    ActionType["Eat"] = "eat";
    ActionType["Interact"] = "interact";
    ActionType["Store"] = "store";
    ActionType["Retrieve"] = "retrieve";
})(ActionType || (ActionType = {}));
export var ClientEventType;
(function (ClientEventType) {
    ClientEventType["TaskCompleted"] = "task_completed";
    ClientEventType["TaskFailed"] = "task_failed";
    ClientEventType["TaskAborted"] = "task_aborted";
    ClientEventType["Death"] = "death";
    ClientEventType["PanicRetreat"] = "panic_retreat";
})(ClientEventType || (ClientEventType = {}));
//# sourceMappingURL=models.js.map