import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";

// Emulate __dirname for ES Modules
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Configure these to match your setup
const MC_VERSION = "1.19";
const SOURCE_DIR = path.resolve(
    __dirname,
    `../node_modules/minecraft-assets/data/${MC_VERSION}`,
);
const DEST_DIR = path.resolve(__dirname, `../public/assets/items`);

function extractAssets() {
    console.log(`[+] Starting asset extraction for Minecraft ${MC_VERSION}...`);

    // Safety check: Make sure the package is actually installed and the version exists
    if (!fs.existsSync(SOURCE_DIR)) {
        console.error(
            `[!] Fuck. Couldn't find the assets folder for version ${MC_VERSION}.`,
        );
        console.error(`[!] Looked in: ${SOURCE_DIR}`);
        console.error(
            `[!] Did you run 'npm install minecraft-assets'? Or maybe check if the version folder is named '1.19.0' or similar in node_modules.`,
        );
        process.exit(1);
    }

    // Ensure our UI destination folder exists
    if (!fs.existsSync(DEST_DIR)) {
        fs.mkdirSync(DEST_DIR, { recursive: true });
    }

    // In Minecraft, some inventory items use the block texture (like dirt, cobblestone),
    // while others use dedicated item textures (like swords, apples). We need both.
    const itemDir = path.join(SOURCE_DIR, "items");
    const blockDir = path.join(SOURCE_DIR, "blocks");

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

                // Copy the file, overwriting if it already exists
                fs.copyFileSync(srcFile, destFile);
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
