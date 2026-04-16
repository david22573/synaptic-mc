import vm from "node:vm";
import { Vec3 } from "vec3";

const code = process.argv[2];

if (!code) {
    console.error("No code provided");
    process.exit(1);
}

const mockBot = {
    registry: {
        blocksByName: {
            log: { id: 1 }
        }
    },
    findBlock: () => null,
    pathfinder: {
        setGoal: () => {}
    },
    inventory: {
        items: () => []
    },
    on: () => {},
    removeListener: () => {},
    emit: () => {},
    // ... add more mocks as needed
};

const mockGoals = {
    GoalNear: class {},
    GoalBlock: class {},
};

try {
    const wrappedCode = `(async (bot, goals, Vec3, signal) => { 
        ${code} 
    })`;

    const script = new vm.Script(wrappedCode);
    const context = vm.createContext({
        console: { log: () => {}, error: () => {}, warn: () => {} },
    });

    const compiledFn = script.runInContext(context, {
        timeout: 1000,
    });

    // We don't actually await it because it might involve network/wait
    // But we check if it compiles and returns a promise
    const promise = compiledFn(mockBot, mockGoals, Vec3, new AbortController().signal);
    if (!(promise instanceof Promise)) {
        throw new Error("Skill must return a Promise (be async)");
    }

    console.log("VALID");
    process.exit(0);
} catch (err: any) {
    console.error(`VALIDATION_FAILED: ${err.message}`);
    process.exit(1);
}
