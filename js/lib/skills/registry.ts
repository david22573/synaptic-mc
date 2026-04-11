// js/lib/skills/registry.ts

export interface ExecutableSkill {
    name: string;
    description: string;
    js_code: string;
}

type SkillFunction = (bot: any, args: any) => Promise<void>;

export class SkillRegistry {
    private skills: Map<string, SkillFunction> = new Map();

    /**
     * Registers a new skill by compiling the JS string into an executable async function.
     * The runtime injects the Mineflayer `bot` instance and task `args`.
     */
    public register(skill: ExecutableSkill): void {
        try {
            const fn = new Function(
                "bot",
                "args",
                `return (async () => { ${skill.js_code} })();`,
            ) as SkillFunction;
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
    public async execute(name: string, bot: any, args: any): Promise<void> {
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
