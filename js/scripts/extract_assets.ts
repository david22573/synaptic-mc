import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";

// Emulate __dirname for ES Modules
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const TARGET_VERSION = "1.19";
const DATA_DIR = path.resolve(
    __dirname,
    "../node_modules/minecraft-assets/minecraft-assets/data",
);
const DEST_DIR = path.resolve(__dirname, "../public/assets/items");
const COPY_RETRIES = 3;

function copyFileWithRetry(srcFile: string, destFile: string) {
    let lastError: unknown;

    for (let attempt = 1; attempt <= COPY_RETRIES; attempt++) {
        try {
            fs.copyFileSync(srcFile, destFile);
            return;
        } catch (error) {
            lastError = error;

            try {
                // Windows can intermittently fail `copyFileSync` against pnpm-managed
                // hardlinked package files. Fall back to a plain read/write copy.
                const data = fs.readFileSync(srcFile);
                fs.writeFileSync(destFile, data);
                return;
            } catch (fallbackError) {
                lastError = fallbackError;
            }
        }
    }

    throw lastError;
}

function findBestVersionFolder(): string | null {
    if (!fs.existsSync(DATA_DIR)) {
        console.error(`[!] Fatal: Cannot find ${DATA_DIR}`);
        console.error(`[!] Did you run 'npm install minecraft-assets'?`);
        return null;
    }

    const folders = fs.readdirSync(DATA_DIR);

    // 1. Try exact match first (e.g., "1.19")
    if (folders.includes(TARGET_VERSION)) {
        return path.join(DATA_DIR, TARGET_VERSION);
    }

    // 2. Try fuzzy patch match (e.g., "1.19.4")
    // Sorting reverse ensures we grab the highest patch version available
    const fuzzyMatches = folders
        .filter((f) => f.startsWith(`${TARGET_VERSION}.`))
        .sort()
        .reverse();
    if (fuzzyMatches.length > 0) {
        console.log(
            `[i] Exact match for ${TARGET_VERSION} not found. Using closest available: ${fuzzyMatches[0]}`,
        );
        return path.join(DATA_DIR, fuzzyMatches[0]!);
    }

    // 3. Complete failure
    console.error(
        `[!] Fatal: Could not find any assets for ${TARGET_VERSION}.x`,
    );
    console.error(`[i] Available versions in package: ${folders.join(", ")}`);
    return null;
}

function extractAssets() {
    console.log(
        `[+] Starting asset extraction for Minecraft ${TARGET_VERSION} branch...`,
    );

    const sourceDir = findBestVersionFolder();
    if (!sourceDir) {
        process.exit(1);
    }

    // Ensure our UI destination folder exists
    if (!fs.existsSync(DEST_DIR)) {
        fs.mkdirSync(DEST_DIR, { recursive: true });
    }

    // In Minecraft, some inventory items use the block texture (like dirt, cobblestone),
    // while others use dedicated item textures (like swords, apples). We need both.
    const itemDir = path.join(sourceDir, "items");
    const blockDir = path.join(sourceDir, "blocks");

    let copied = 0;

    const copyFiles = (srcDir: string) => {
        if (!fs.existsSync(srcDir)) {
            console.warn(`[!] Warning: Source directory not found: ${srcDir}`);
            return;
        }

        const files = fs.readdirSync(srcDir);
        for (const file of files) {
            if (file.endsWith(".png")) {
                const srcFile = path.join(srcDir, file);
                const destFile = path.join(DEST_DIR, file);

                copyFileWithRetry(srcFile, destFile);
                copied++;
            }
        }
    };

    console.log(`[+] Copying item textures...`);
    copyFiles(itemDir);

    console.log(`[+] Copying block textures...`);
    copyFiles(blockDir);

    console.log(
        `[+] Hell yeah. Successfully ripped ${copied} textures into ${DEST_DIR}`,
    );
}

extractAssets();
