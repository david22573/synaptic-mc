"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
var svelte_1 = require("svelte");
var App_svelte_1 = require("./App.svelte");
require("./app.css");
var app = (0, svelte_1.mount)(App_svelte_1.default, {
    target: document.getElementById("app"),
    props: {},
});
exports.default = app;
