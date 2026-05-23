/**
 * Appx guardrails for Pi sessions.
 *
 * These are intentionally first-party and project-local. They avoid silent
 * third-party code execution while still giving Appx a UI-mediated approval
 * path for destructive commands through agent-server's extension UI bridge.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const destructiveBashPatterns: Array<{ pattern: RegExp; label: string }> = [
	{ pattern: /\brm\s+(-[^\s]*r[^\s]*f|-rf|-fr|--recursive)\b/i, label: "recursive delete" },
	{ pattern: /\bsudo\b/i, label: "sudo" },
	{ pattern: /\b(chmod|chown)\b\s+(-R|--recursive)\b/i, label: "recursive permission/owner change" },
	{ pattern: /\bchmod\b[^\n;&|]*\b777\b/i, label: "world-writable permissions" },
	{ pattern: /\b(dd|mkfs|mount|umount|fdisk|parted)\b/i, label: "disk/system command" },
	{ pattern: /\bkill\s+-9\b/i, label: "force kill" },
];

const protectedPathFragments = [
	"/.appx-internals/",
	"/etc/appx/",
	"/usr/local/bin/appx",
	"/home/opencode/.pi/agent/auth.json",
	"/home/opencode/.config/opencode/",
	"~/.pi/agent/auth.json",
	".git/",
	"node_modules/",
	".env",
	".env.",
	".pem",
	".key",
	".p12",
];

function asString(value: unknown): string {
	return typeof value === "string" ? value : "";
}

function commandRisk(command: string): string | undefined {
	return destructiveBashPatterns.find((entry) => entry.pattern.test(command))?.label;
}

function pathRisk(filePath: string): string | undefined {
	const normalized = filePath.replaceAll("\\", "/");
	return protectedPathFragments.find((fragment) => normalized.includes(fragment));
}

export default function appxGuardrails(pi: ExtensionAPI) {
	pi.on("session_start", async (_event, ctx) => {
		if (!ctx.hasUI) return;
		ctx.ui.setStatus("appx", "Appx guardrails active");
	});

	pi.on("tool_call", async (event, ctx) => {
		if (event.toolName === "bash") {
			const command = asString(event.input.command);
			const risk = commandRisk(command);
			if (!risk) return undefined;

			if (!ctx.hasUI) {
				return { block: true, reason: `Blocked ${risk} command because no UI approval channel is available.` };
			}

			const ok = await ctx.ui.confirm("Approve command?", `${risk}\n\n${command}`);
			if (!ok) return { block: true, reason: `Blocked ${risk} command by user decision.` };
			return undefined;
		}

		if (event.toolName === "write" || event.toolName === "edit") {
			const filePath = asString(event.input.path);
			const risk = pathRisk(filePath);
			if (!risk) return undefined;

			if (ctx.hasUI) {
				ctx.ui.notify(`Blocked write to protected path: ${filePath}`, "warning");
			}
			return { block: true, reason: `Path is protected by Appx guardrails (${risk}).` };
		}

		return undefined;
	});
}
