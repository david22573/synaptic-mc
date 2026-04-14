// js/lib/tasks/handlers/use_skill.ts
import vm from "node:vm";
import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";
import { ExecutionError } from "../../errors.js";
import { TaskAbortError, isAbortError } from "../../errors.js";
import type { TaskContext } from "../registry.js";
import { log } from "../../logger.js";

const { goals } = pkg;

interface SkillPayload {
    name: string;
    description: string;
    js_code: string;
}

export async function handleUseSkill(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal } = ctx;
    const skillName = intent.target?.name;

    if (!skillName || skillName === "none") {
        throw new ExecutionError(
            "use_skill requires a non-empty target name (the skill name)",
            "INVALID",
            0,
        );
    }

    log.info("[UseSkill] Fetching skill from vector store", {
        skill: skillName,
    });

    // Fetch the stored JS code from the Go server.
    // Uses the same base URL as the config endpoint.
    let skillPayload: SkillPayload;
    try {
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 5000);

        const response = await fetch(
            `http://127.0.0.1:8080/api/skills/${encodeURIComponent(skillName)}`,
            { signal: controller.signal },
        );
        clearTimeout(timeoutId);

        if (!response.ok) {
            throw new ExecutionError(
                `Skill '${skillName}' not found in vector store (HTTP ${response.status})`,
                "MISSING_RESOURCE",
                0,
            );
        }

        skillPayload = (await response.json()) as SkillPayload;
    } catch (err: any) {
        if (err instanceof ExecutionError) throw err;
        throw new ExecutionError(
            `Failed to fetch skill '${skillName}': ${err.message}`,
            "MISSING_RESOURCE",
            0,
        );
    }

    if (!skillPayload.js_code || skillPayload.js_code.trim() === "") {
        throw new ExecutionError(
            `Skill '${skillName}' exists but has no executable code`,
            "INVALID",
            0,
        );
    }

    log.info("[UseSkill] Executing skill", {
        skill: skillName,
        desc: skillPayload.description,
        codeLen: skillPayload.js_code.length,
    });

    // Compile and execute in a vm context.
    try {
        const script = new vm.Script(
            `(async (bot, goals, Vec3, signal) => { ${skillPayload.js_code} })`,
        );

        const context = vm.createContext({
            console,
            // Add other globals if necessary
        });

        const compiledFn = script.runInContext(context, {
            timeout: 5000,
        });

        await compiledFn(bot, goals, Vec3, signal);
    } catch (err: any) {
        if (isAbortError(err)) {
            throw new TaskAbortError("aborted", 0.5);
        }
        throw new ExecutionError(
            `Skill '${skillName}' failed at runtime: ${err.message}`,
            "error",
            0,
        );
    }

    log.info("[UseSkill] Skill completed successfully", { skill: skillName });
}
