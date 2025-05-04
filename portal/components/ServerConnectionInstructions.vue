<template>
  <v-card v-if="showSection" class="mt-6 connection-instructions-card">
    <v-card-title class="text-h6">Connection Instructions</v-card-title>
    <v-card-text>
      <v-tabs v-model="tab" grow density="compact">
        <v-tab value="cursor" class="text-caption">Cursor</v-tab>
        <v-tab value="vscode" class="text-caption">VSCode</v-tab>
        <v-tab value="claude" class="text-caption">Claude Desktop</v-tab>
        <v-tab value="windsurf" class="text-caption">Windsurf</v-tab>
      </v-tabs>

      <v-window v-model="tab" class="mt-4">
        <v-window-item value="cursor">
          <p class="mb-2 text-caption">
            Add the following to your Cursor configuration (settings.json):
          </p>
          <v-code tag="pre" class="pa-2 rounded code-block">
            {{ cursorConfig }}
          </v-code>
        </v-window-item>

        <v-window-item value="vscode">
          <p class="mb-2 text-caption">
            Add the following to your VSCode configuration (settings.json):
          </p>
          <v-code tag="pre" class="pa-2 rounded code-block">
            {{ vscodeConfig }}
          </v-code>
        </v-window-item>

        <v-window-item value="claude">
          <p class="mb-2 text-caption">
            Add the following to your Claude Desktop configuration
            (config.json):
          </p>
          <v-code tag="pre" class="pa-2 rounded code-block">
            {{ claudeConfig }}
          </v-code>
        </v-window-item>

        <v-window-item value="windsurf">
          <p class="mb-2 text-caption">
            Add the following to your Windsurf configuration (config.json):
          </p>
          <v-code tag="pre" class="pa-2 rounded code-block">
            {{ windsurfConfig }}
          </v-code>
        </v-window-item>
      </v-window>
      <p class="mt-4 text-caption">
        Replace
        <code v-if="!isAuthenticated" class="text-caption api-key-placeholder"
          >xxxxxxxx (Get API Key)</code
        >
        <router-link v-else to="/keys" class="text-caption api-key-link"
          >xxxxxxxx (Get API Key)</router-link
        >
        with your API key from the
        <router-link to="/keys" class="text-caption api-key-link"
          >My API Keys</router-link
        >
        page.
      </p>
    </v-card-text>
  </v-card>
  <v-alert
    v-else-if="requiresSubscription && !isSubscribed && !hasImplicitAccess"
    type="info"
    variant="tonal"
    class="mt-6"
    density="compact"
  >
    Subscription required to view connection instructions.
    <!-- Simple button emitting the event -->
    <v-btn
      variant="text"
      size="small"
      color="primary"
      @click="$emit('subscribe-now')"
    >
      Subscribe
    </v-btn>
  </v-alert>
  <!-- Add a message for owners/admins viewing a subscription server, if desired -->
  <!--
  <v-alert v-else-if="requiresSubscription && hasImplicitAccess" type="info" variant="tonal" class="mt-6" density="compact">
    As an owner/admin, you have default access. Connection instructions are shown above.
  </v-alert>
   -->
</template>

<script setup lang="ts">
import { ref, computed } from "vue";

const props = defineProps<{
  gatewayBaseUrl: string; // e.g., http://gate4.ai
  serverSlug: string; // For potential future use in configs
  isAuthenticated: boolean;
  requiresSubscription: boolean;
  isSubscribed: boolean;
  hasImplicitAccess: boolean; // New prop for owner/admin status
}>();

// Emit event when the subscribe button is clicked
defineEmits(["subscribe-now"]);

const tab = ref("cursor"); // Default tab

const apiKeyPlaceholder = "xxxxxxxx"; // Keep placeholder for configs

const effectiveGatewayUrl = computed(() => {
  // Ensure no trailing slash for consistency
  return props.gatewayBaseUrl.replace(/\/$/, "");
});

const sseEndpoint = computed(() => `${effectiveGatewayUrl.value}/sse`);

// Generate configuration strings dynamically
const cursorConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        "gate4.ai": {
          url: `${sseEndpoint.value}?key=${apiKeyPlaceholder}`,
        },
      },
    },
    null,
    2 // Indent with 2 spaces for readability
  )
);

const vscodeConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        "gate4.ai": {
          type: "sse",
          url: `${sseEndpoint.value}?key=${apiKeyPlaceholder}`,
        },
      },
    },
    null,
    2
  )
);

const claudeConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        // Server name here is arbitrary for Claude Desktop, use 'gate4ai' for consistency
        gate4ai: {
          command: "npx",
          args: ["mcp-remote", `${sseEndpoint.value}?key=${apiKeyPlaceholder}`],
        },
      },
    },
    null,
    2
  )
);

const windsurfConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        "gate4.ai": {
          url: `${sseEndpoint.value}?key=${apiKeyPlaceholder}`,
        },
      },
    },
    null,
    2
  )
);

// Determine if the connection section should be shown
const showSection = computed(() => {
  // --- LOGGING ---
  console.log(`[ServerConnectionInstructions] showSection computed:`, {
    requiresSubscription: props.requiresSubscription,
    isSubscribed: props.isSubscribed,
    hasImplicitAccess: props.hasImplicitAccess,
  });

  // Show if server doesn't require subscription
  if (!props.requiresSubscription) {
    console.log(
      `[ServerConnectionInstructions] showSection result: true (requiresSubscription is false)`
    );
    return true;
  }
  // Show if server requires subscription AND (user is subscribed OR user has implicit access)
  const result =
    props.requiresSubscription &&
    (props.isSubscribed || props.hasImplicitAccess);
  console.log(
    `[ServerConnectionInstructions] showSection result: ${result}`,
    `(isSubscribed || hasImplicitAccess) = ${
      props.isSubscribed || props.hasImplicitAccess
    }`
  );
  return result;
});
</script>

<style scoped>
.connection-instructions-card {
  border: 1px solid rgba(0, 0, 0, 0.12); /* Add a subtle border */
  background-color: #f9f9f9; /* Slightly off-white background */
}

/* Style for the code blocks */
.code-block {
  background-color: #2d2d2d; /* Dark background */
  color: #f0f0f0; /* Light text */
  font-family: "Courier New", Courier, monospace; /* Monospace font */
  font-size: 0.85rem; /* Slightly smaller font */
  white-space: pre; /* Preserve whitespace and prevent wrapping */
  overflow-x: auto; /* Add horizontal scroll if needed */
  border-radius: 4px;
}

/* Specific styling for the API key placeholder/link in the text below */
.api-key-placeholder {
  background-color: #e0e0e0;
  padding: 1px 4px;
  border-radius: 3px;
  font-family: monospace;
}
.api-key-link {
  font-family: monospace;
  text-decoration: none;
  color: #1867c0; /* Vuetify primary color */
  border-bottom: 1px dashed #1867c0;
}
.api-key-link:hover {
  color: #0d47a1;
  border-bottom: 1px solid #0d47a1;
}
</style>
