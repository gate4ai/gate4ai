<template>
  <div>
    <h2 class="text-h4 mb-4">REST API Endpoints</h2>

    <v-expansion-panels v-if="endpoints && endpoints.length > 0">
      <v-expansion-panel v-for="endpoint in endpoints" :key="endpoint.path">
        <v-expansion-panel-title>
          <div class="d-flex align-center">
            <v-chip
              :color="getMethodColor(endpoint.method)"
              size="small"
              class="mr-2"
            >
              {{ endpoint.method }}
            </v-chip>
            <span class="text-subtitle-1">{{ endpoint.path }}</span>
          </div>
        </v-expansion-panel-title>
        <v-expansion-panel-text>
          <div v-if="endpoint.description" class="my-2">
            <p>{{ endpoint.description }}</p>
          </div>

          <!-- Parameters section -->
          <h3 v-if="hasParameters(endpoint)" class="text-h6 mt-4 mb-2">
            Parameters
          </h3>
          <v-table
            v-if="endpoint.queryParams && endpoint.queryParams.length > 0"
          >
            <thead>
              <tr>
                <th>Query Parameter</th>
                <th>Type</th>
                <th>Required</th>
                <th>Description</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="param in endpoint.queryParams" :key="param.name">
                <td>{{ param.name }}</td>
                <td>
                  <code>{{ param.type }}</code>
                </td>
                <td>
                  <v-icon v-if="param.required" color="success">
                    mdi-check
                  </v-icon>
                  <v-icon v-else color="grey"> mdi-minus </v-icon>
                </td>
                <td>{{ param.description }}</td>
              </tr>
            </tbody>
          </v-table>

          <!-- Request body section -->
          <div v-if="endpoint.requestBody" class="mt-4">
            <h3 class="text-h6 mb-2">Request Body</h3>
            <v-card variant="outlined" class="pa-2">
              <p
                v-if="endpoint.requestBody.description"
                class="text-subtitle-2 mb-2"
              >
                {{ endpoint.requestBody.description }}
              </p>
              <pre
                v-if="endpoint.requestBody.example"
                class="bg-grey-lighten-4 pa-2 rounded"
                >{{ endpoint.requestBody.example }}</pre
              >
            </v-card>
          </div>

          <!-- Response section -->
          <div
            v-if="endpoint.responses && endpoint.responses.length > 0"
            class="mt-4"
          >
            <h3 class="text-h6 mb-2">Responses</h3>
            <div
              v-for="(response, index) in endpoint.responses"
              :key="index"
              class="mb-2"
            >
              <v-card variant="outlined" class="pa-2">
                <div class="d-flex align-center mb-2">
                  <v-chip
                    :color="getStatusColor(response.statusCode)"
                    size="small"
                    class="mr-2"
                  >
                    {{ response.statusCode }}
                  </v-chip>
                  <span class="text-subtitle-1">{{
                    response.description
                  }}</span>
                </div>
                <pre
                  v-if="response.example"
                  class="bg-grey-lighten-4 pa-2 rounded"
                  >{{ response.example }}</pre
                >
              </v-card>
            </div>
          </div>
        </v-expansion-panel-text>
      </v-expansion-panel>
    </v-expansion-panels>

    <div v-else class="text-center py-4">
      <v-icon size="large" color="grey" class="mb-2">mdi-api</v-icon>
      <p class="text-body-1">No REST endpoints available for this server</p>
    </div>
  </div>
</template>

<script setup lang="ts">
// Import the RestEndpoint type from the server utils
import type { RestEndpoint } from "~/utils/server";

// Props definition
defineProps({
  endpoints: {
    type: Array as () => RestEndpoint[],
    default: () => [],
  },
  isAuthenticated: {
    type: Boolean,
    default: false,
  },
});

// Helper function to determine method color
function getMethodColor(method: string): string {
  switch (method.toUpperCase()) {
    case "GET":
      return "success";
    case "POST":
      return "primary";
    case "PUT":
      return "warning";
    case "PATCH":
      return "amber";
    case "DELETE":
      return "error";
    default:
      return "grey";
  }
}

// Helper function to determine status code color
function getStatusColor(statusCode: number): string {
  if (statusCode >= 200 && statusCode < 300) {
    return "success";
  } else if (statusCode >= 300 && statusCode < 400) {
    return "info";
  } else if (statusCode >= 400 && statusCode < 500) {
    return "warning";
  } else if (statusCode >= 500) {
    return "error";
  }
  return "grey";
}

// Helper function to check if endpoint has any parameters
function hasParameters(endpoint: RestEndpoint): boolean {
  return !!(endpoint.queryParams && endpoint.queryParams.length > 0);
}
</script>
