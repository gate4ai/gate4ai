<template>
  <v-row justify="center" align="center" class="auth-container">
    <v-col cols="12" sm="8" md="6" lg="4">
      <v-card class="pa-4">
        <v-card-title class="text-center text-h4 mb-4">
          Reset Password
        </v-card-title>
        
        <v-form ref="form">
          <v-card-text>
            <p class="text-body-1 mb-4">
              I haven't set up the mail server yet. I can generate a new password for you manually. Please write to me on Telegram: @nebotov.
            </p>
            
            
            <div class="text-center">
              <v-btn
                variant="text"
                color="primary"
                to="/login"
                class="ml-2"
              >
                Back to Login
              </v-btn>
            </div>
          </v-card-text>
        </v-form>
        
        <div class="pa-4 text-center">
          <v-icon
            color="success"
            size="large"
            class="mb-4"
          >
            mdi-check-circle
          </v-icon>
          
          
          <v-btn
            color="primary"
            to="/login"
            class="mt-4"
          >
            Back to Login
          </v-btn>
        </div>
      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">

definePageMeta({
  title: 'Forgot Password',
  layout: 'default',
});

</script>

<style scoped>
.auth-container {
  min-height: calc(100vh - 200px);
}
</style> 