<template>
  <div>
    <!-- Hero Section -->
    <v-row class="hero-section py-12">
      <v-col cols="12" md="6" class="d-flex flex-column justify-center">
        <h1 class="text-h2 font-weight-bold mb-4">gate4.ai</h1>
        <p class="text-h5 mb-6">
          Enterprise-grade management for MCP servers and OpenAPI access
        </p>
        <div>
          <v-btn
            color="primary"
            size="large"
            class="mr-4"
            to="/servers"
          >
            Explore Catalog
          </v-btn>
          <v-btn
            variant="outlined"
            size="large"
            to="/register"
          >
            Get Started
          </v-btn>
        </div>
      </v-col>
      <v-col cols="12" md="6" class="d-flex align-center justify-center">
        <v-img
          src="/images/logo.svg"
          alt="gate4.ai"
          width="200"
          height="200"
          contain
        />
      </v-col>
    </v-row>

    <!-- Features Section -->
    <v-row class="py-12">
      <v-col cols="12" class="text-center mb-8">
        <h2 class="text-h4 font-weight-bold">Key Features</h2>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-shield-account</v-icon>
            Access Control
          </v-card-title>
          <v-card-text>
            Manage access to MCP servers with fine-grained permissions for users and API keys.
          </v-card-text>
        </v-card>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-view-grid</v-icon>
            Server Catalog
          </v-card-title>
          <v-card-text>
            Discover and connect to MCP servers through our comprehensive catalog.
          </v-card-text>
        </v-card>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-api</v-icon>
            OpenAPI Integration
          </v-card-title>
          <v-card-text>
            Transform MCP calls to OpenAPI for seamless integration with LLM servers.
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>

    <!-- Distribution Options -->
    <v-row class="py-12 bg-grey-lighten-4">
      <v-col cols="12" class="text-center mb-8">
        <h2 class="text-h4 font-weight-bold">Distribution Options</h2>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-cloud</v-icon>
            Cloud Portal
          </v-card-title>
          <v-card-text>
            Access our cloud-hosted catalog mcp servers.
          </v-card-text>
        </v-card>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-github</v-icon>
            Open Source
          </v-card-title>
          <v-card-text>
            Install our open-source library locally for complete control over your deployment.
          </v-card-text>
        </v-card>
      </v-col>
      
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-office-building</v-icon>
            Enterprise
          </v-card-title>
          <v-card-text>
            Deploy our standalone application within your corporate infrastructure with advanced features.
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>
  </div>
</template>

<script setup lang="ts">
definePageMeta({
  title: 'Home',
  layout: 'default',
});
</script>

<style scoped>
.hero-section {
  background: linear-gradient(135deg, #f5f7fa 0%, #c3cfe2 100%);
}
</style> 