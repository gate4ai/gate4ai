<template>
  <div>
    <h2 class="text-h4 mb-4">Available Tools</h2>

    <v-expansion-panels v-if="tools && tools.length > 0">
      <v-expansion-panel
        v-for="tool in tools"
        :key="tool.id || tool.name" 
      >
        <v-expansion-panel-title>
          <div class="d-flex flex-column align-start">
            <span class="text-subtitle-1">{{ tool.name }}</span>
            <span v-if="tool.description" class="text-caption text-grey">{{ tool.description }}</span>
          </div>
        </v-expansion-panel-title>
        <v-expansion-panel-text>
          <h3 class="text-h6 mt-4 mb-2">Parameters</h3>
          <!-- Check if parameters array exists and has items -->
          <v-table v-if="tool.parameters && tool.parameters.length > 0">
            <thead>
              <tr>
                <th>Name</th>
                <th>Type</th>
                <th>Required</th>
                <th>Description</th>
              </tr>
            </thead>
            <tbody>
              <!-- Iterate over the parameters array -->
              <tr v-for="param in tool.parameters" :key="param.id || param.name"> <!-- Use id if available -->
                <td>{{ param.name }}</td>
                <td><code>{{ param.type }}</code></td>
                <td>
                  <!-- Directly use the required flag from the parameter object -->
                  <v-icon v-if="param.required" color="success">
                    mdi-check
                  </v-icon>
                  <v-icon v-else color="grey">
                    mdi-minus
                  </v-icon>
                </td>
                <td>{{ param.description }}</td>
              </tr>
            </tbody>
          </v-table>
          <div v-else class="text-subtitle-2 text-grey">
            No parameters defined
          </div>
        </v-expansion-panel-text>
      </v-expansion-panel>
    </v-expansion-panels>

    <div v-else class="text-center py-4">
      <v-icon size="large" color="grey" class="mb-2">mdi-toolbox-outline</v-icon>
      <p class="text-body-1">No tools available for this server</p>
    </div>
  </div>
</template>

<script setup lang="ts">
// Import the type that is actually being passed (ServerTool)
import type { ServerTool } from '~/utils/server';

// Props definition using the correct type
const _props = defineProps({
  tools: {
    type: Array as () => ServerTool[], // Expecting ServerTool structure
    default: () => []
  },
  // isAuthenticated might not be needed directly here, remove if unused
  // isAuthenticated: {
  //   type: Boolean,
  //   default: false
  // }
});

// isRequired function is no longer needed as we access param.required directly
</script>