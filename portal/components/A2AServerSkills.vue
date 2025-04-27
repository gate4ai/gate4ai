<template>
  <div>
    <h2 class="text-h4 mb-4">Agent Skills</h2>

    <v-expansion-panels v-if="skills && skills.length > 0">
      <v-expansion-panel v-for="skill in skills" :key="skill.id">
        <v-expansion-panel-title>
          <div class="d-flex flex-column align-start">
            <span class="text-subtitle-1">{{ skill.name }}</span>
            <span v-if="skill.description" class="text-caption text-grey">{{
              skill.description
            }}</span>
          </div>
        </v-expansion-panel-title>
        <v-expansion-panel-text>
          <div v-if="skill.tags && skill.tags.length > 0" class="mb-4">
            <h3 class="text-subtitle-1 mb-2">Tags</h3>
            <v-chip-group>
              <v-chip
                v-for="tag in skill.tags"
                :key="tag"
                color="info"
                size="small"
              >
                {{ tag }}
              </v-chip>
            </v-chip-group>
          </div>

          <div v-if="skill.examples && skill.examples.length > 0" class="mb-4">
            <h3 class="text-subtitle-1 mb-2">Examples</h3>
            <v-list>
              <v-list-item
                v-for="(example, index) in skill.examples"
                :key="index"
              >
                <v-list-item-title>
                  <v-icon small class="mr-2">mdi-format-quote-open</v-icon>
                  {{ example }}
                </v-list-item-title>
              </v-list-item>
            </v-list>
          </div>

          <div
            v-if="skill.inputModes && skill.inputModes.length > 0"
            class="mb-2"
          >
            <h3 class="text-subtitle-1 mb-1">Input Modes</h3>
            <v-chip-group>
              <v-chip
                v-for="mode in skill.inputModes"
                :key="mode"
                color="primary"
                size="small"
              >
                {{ mode }}
              </v-chip>
            </v-chip-group>
          </div>

          <div v-if="skill.outputModes && skill.outputModes.length > 0">
            <h3 class="text-subtitle-1 mb-1">Output Modes</h3>
            <v-chip-group>
              <v-chip
                v-for="mode in skill.outputModes"
                :key="mode"
                color="success"
                size="small"
              >
                {{ mode }}
              </v-chip>
            </v-chip-group>
          </div>
        </v-expansion-panel-text>
      </v-expansion-panel>
    </v-expansion-panels>

    <div v-else class="text-center py-4">
      <v-icon size="large" color="grey" class="mb-2">mdi-brain</v-icon>
      <p class="text-body-1">No A2A skills available for this server</p>
    </div>
  </div>
</template>

<script setup lang="ts">
// Import the AgentSkill type from the shared schema
import type { AgentSkill } from "~/utils/server";

// Props definition
defineProps({
  skills: {
    type: Array as () => AgentSkill[],
    default: () => [],
  },
  isAuthenticated: {
    type: Boolean,
    default: false,
  },
});
</script>
