// js/lib/skills/registry.ts

import vm from "node:vm";
import { type Bot } from "mineflayer";
import { Vec3 } from "vec3";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export interface ExecutableSkill {
    name: string;
    description: string;
    js_code: string;
}

type SkillFunction = (bot: Bot, args: any) => Promise<void>;

export class SkillRegistry {
    private skills: Map<string, SkillFunction> = new Map();

    /**
     * Registers a new skill by compiling the JS string into an executable async function
     * using Node's vm module.
     */
    public register(skill: ExecutableSkill): void {
        try {
            // Compile the script outside the execution to catch syntax errors early.
            // We wrap it in an async function that receives the injected bindings.
            const script = new vm.Script(
                `(async (bot, args, goals, Vec3) => { ${skill.js_code} })`,
            );

            const fn: SkillFunction = async (bot: Bot, args: any) => {
                const context = vm.createContext({
                    console,
                    // Optionally restrict available globals further here
                });

                // Run the script with a timeout to prevent infinite loops.
                const compiledFn = script.runInContext(context, {
                    timeout: 5000, // Compilation/setup timeout
                });

                // Execute the actual skill logic.
                // Note: vm's timeout doesn't cover async execution time, 
                // so the skill itself should handle internal timeouts/signals.
                await compiledFn(bot, args, goals, Vec3);
            };

            this.skills.set(skill.name, fn);
            console.log(
                `[SkillRegistry] Registered executable skill: ${skill.name}`,
            );
        } catch (err) {
            console.error(
                `[SkillRegistry] Failed to compile skill ${skill.name}:`,
                err,
            );
        }
    }

    /**
     * Executes a retrieved skill by ID.
     */
    public async execute(name: string, bot: Bot, args: any): Promise<void> {
        const skillFn = this.skills.get(name);
        if (!skillFn) {
            throw new Error(
                `[SkillRegistry] Execution failed. Skill '${name}' not found in runtime registry.`,
            );
        }

        console.log(`[SkillRegistry] Executing: ${name}`, args);
        await skillFn(bot, args);
    }

    /**
     * Returns a list of currently loaded skill IDs.
     */
    public getAvailable(): string[] {
        return Array.from(this.skills.keys());
    }

    /**
     * Bulk loads skills synchronized from the Go vector store.
     */
    public loadFromDatabaseData(skillsData: ExecutableSkill[]): void {
        for (const skill of skillsData) {
            this.register(skill);
        }
    }
}
