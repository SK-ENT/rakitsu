// Verbatim excerpt of dist/schema.d.ts from @zed-industries/agent-client-protocol@0.4.5
// (Apache-2.0, Copyright Zed Industries, Inc.). See README.md for provenance.

export declare const AGENT_METHODS: {
    readonly authenticate: "authenticate";
    readonly initialize: "initialize";
    readonly session_cancel: "session/cancel";
    readonly session_load: "session/load";
    readonly session_new: "session/new";
    readonly session_prompt: "session/prompt";
    readonly session_set_mode: "session/set_mode";
    readonly session_set_model: "session/set_model";
};

export declare const CLIENT_METHODS: {
    readonly fs_read_text_file: "fs/read_text_file";
    readonly fs_write_text_file: "fs/write_text_file";
    readonly session_request_permission: "session/request_permission";
    readonly session_update: "session/update";
    readonly terminal_create: "terminal/create";
    readonly terminal_kill: "terminal/kill";
    readonly terminal_output: "terminal/output";
    readonly terminal_release: "terminal/release";
    readonly terminal_wait_for_exit: "terminal/wait_for_exit";
};

/**
 * Content produced by a tool call.
 *
 * Tool calls can produce different types of content including
 * standard content blocks (text, images) or file diffs.
 *
 * See protocol docs: [Content](https://agentclientprotocol.com/protocol/tool-calls#content)
 */
export type ToolCallContent = {
    /**
     * Content blocks represent displayable information in the Agent Client Protocol.
     *
     * They provide a structured way to handle various types of user-facing content—whether
     * it's text from language models, images for analysis, or embedded resources for context.
     *
     * Content blocks appear in:
     * - User prompts sent via `session/prompt`
     * - Language model output streamed through `session/update` notifications
     * - Progress updates and results from tool calls
     *
     * This structure is compatible with the Model Context Protocol (MCP), enabling
     * agents to seamlessly forward content from MCP tool outputs without transformation.
     *
     * See protocol docs: [Content](https://agentclientprotocol.com/protocol/content)
     */
    content: {
        /**
         * Extension point for implementations
         */
        _meta?: {
            [k: string]: unknown;
        };
        annotations?: Annotations | null;
        text: string;
        type: "text";
    } | {
        /**
         * Extension point for implementations
         */
        _meta?: {
            [k: string]: unknown;
        };
        annotations?: Annotations | null;
        data: string;
        mimeType: string;
        type: "image";
        uri?: string | null;
    } | {
        /**
         * Extension point for implementations
         */
        _meta?: {
            [k: string]: unknown;
        };
        annotations?: Annotations | null;
        data: string;
        mimeType: string;
        type: "audio";
    } | {
        /**
         * Extension point for implementations
         */
        _meta?: {
            [k: string]: unknown;
        };
        annotations?: Annotations | null;
        description?: string | null;
        mimeType?: string | null;
        name: string;
        size?: number | null;
        title?: string | null;
        type: "resource_link";
        uri: string;
    } | {
        /**
         * Extension point for implementations
         */
        _meta?: {
            [k: string]: unknown;
        };
        annotations?: Annotations | null;
        resource: EmbeddedResourceResource;
        type: "resource";
    };
    type: "content";
} | {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * The new content after modification.
     */
    newText: string;
    /**
     * The original content (None for new files).
     */
    oldText?: string | null;
    /**
     * The file path being modified.
     */
    path: string;
    type: "diff";
} | {
    terminalId: string;
    type: "terminal";
};

/**
 * The sender or recipient of messages and data in a conversation.
 */
export type Role = "assistant" | "user";

/**
 * Resource content that can be embedded in a message.
 */
export type EmbeddedResourceResource = TextResourceContents | BlobResourceContents;

/**
 * Categories of tools that can be invoked.
 *
 * Tool kinds help clients choose appropriate icons and optimize how they
 * display tool execution progress.
 *
 * See protocol docs: [Creating](https://agentclientprotocol.com/protocol/tool-calls#creating)
 */
export type ToolKind = "read" | "edit" | "delete" | "move" | "search" | "execute" | "think" | "fetch" | "switch_mode" | "other";

/**
 * Execution status of a tool call.
 *
 * Tool calls progress through different statuses during their lifecycle.
 *
 * See protocol docs: [Status](https://agentclientprotocol.com/protocol/tool-calls#status)
 */
export type ToolCallStatus = "pending" | "in_progress" | "completed" | "failed";

/**
 * Request for user permission to execute a tool call.
 *
 * Sent when the agent needs authorization before performing a sensitive operation.
 *
 * See protocol docs: [Requesting Permission](https://agentclientprotocol.com/protocol/tool-calls#requesting-permission)
 */
export interface RequestPermissionRequest {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * Available permission options for the user to choose from.
     */
    options: PermissionOption[];
    /**
     * The session ID for this request.
     */
    sessionId: string;
    toolCall: ToolCallUpdate;
}

/**
 * An option presented to the user when requesting permission.
 */
export interface PermissionOption {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * Hint about the nature of this permission option.
     */
    kind: "allow_once" | "allow_always" | "reject_once" | "reject_always";
    /**
     * Human-readable label to display to the user.
     */
    name: string;
    /**
     * Unique identifier for this permission option.
     */
    optionId: string;
}

/**
 * Details about the tool call requiring permission.
 */
export interface ToolCallUpdate {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * Replace the content collection.
     */
    content?: ToolCallContent[] | null;
    /**
     * Update the tool kind.
     */
    kind?: ToolKind | null;
    /**
     * Replace the locations collection.
     */
    locations?: ToolCallLocation[] | null;
    /**
     * Update the raw input.
     */
    rawInput?: {
        [k: string]: unknown;
    };
    /**
     * Update the raw output.
     */
    rawOutput?: {
        [k: string]: unknown;
    };
    /**
     * Update the execution status.
     */
    status?: ToolCallStatus | null;
    /**
     * Update the human-readable title.
     */
    title?: string | null;
    /**
     * The ID of the tool call being updated.
     */
    toolCallId: string;
}

/**
 * A file location being accessed or modified by a tool.
 *
 * Enables clients to implement "follow-along" features that track
 * which files the agent is working with in real-time.
 *
 * See protocol docs: [Following the Agent](https://agentclientprotocol.com/protocol/tool-calls#following-the-agent)
 */
export interface ToolCallLocation {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * Optional line number within the file.
     */
    line?: number | null;
    /**
     * The file path being accessed or modified.
     */
    path: string;
}

/**
 * Optional annotations for the client. The client can use annotations to inform how objects are used or displayed
 */
export interface Annotations {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    audience?: Role[] | null;
    lastModified?: string | null;
    priority?: number | null;
}

/**
 * Text-based resource contents.
 */
export interface TextResourceContents {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    mimeType?: string | null;
    text: string;
    uri: string;
}

/**
 * Binary resource contents.
 */
export interface BlobResourceContents {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    blob: string;
    mimeType?: string | null;
    uri: string;
}

/**
 * Response to a permission request.
 */
export interface RequestPermissionResponse {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * The user's decision on the permission request.
     */
    outcome: {
        outcome: "cancelled";
    } | {
        /**
         * The ID of the option the user selected.
         */
        optionId: string;
        outcome: "selected";
    };
}

/**
 * Notification to cancel ongoing operations for a session.
 *
 * See protocol docs: [Cancellation](https://agentclientprotocol.com/protocol/prompt-turn#cancellation)
 */
export interface CancelNotification {
    /**
     * Extension point for implementations
     */
    _meta?: {
        [k: string]: unknown;
    };
    /**
     * The ID of the session to cancel operations for.
     */
    sessionId: string;
}
