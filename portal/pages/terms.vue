<template>
  <v-container>
    <v-row justify="center">
      <v-col cols="12" md="10" lg="8">
        <v-card class="pa-6">
          <v-card-title class="text-h4 mb-4">Terms of Service</v-card-title>
          
          <v-card-text>
            <h2 class="text-h5 mb-3">Terms of Service (ToS)</h2>
            <h2 class="text-h5 mb-3">Disclaimer</h2>
            <p class="mb-4">
              The service is provided "as is" without any warranties. The developers and owners of gate4.ai are not 
              responsible for any damages resulting from the use of the service. All information and functionality are 
              provided without guarantees of accuracy, reliability, or fitness for a particular purpose.
            </p>
            
            <h2 class="text-h5 mb-3">Limitation of Liability</h2>
            <p class="mb-4">
              The service owner is not liable for: Loss of data, profits, or other damages. Service interruptions or errors. 
              Harm caused to third parties due to the use of the platform. The maximum amount of any claims (if applicable) 
              shall not exceed the fees paid by the user for the last 30 days.
            </p>
            
            <h2 class="text-h5 mb-3">User Responsibility</h2>
            <p class="mb-4">
              The user is fully responsible for their use of the service and its consequences. All responsibility for 
              transmitted data and its processing lies with the user. The user agrees not to use the service for illegal 
              purposes.
            </p>
            
            <h2 class="text-h5 mb-3">No Support or Warranties</h2>
            <p class="mb-4">
              The developers do not guarantee uninterrupted service. Support is provided "as is" and may be discontinued 
              at any time.
            </p>
            
            <h2 class="text-h5 mb-3">Third-Party Content</h2>
            <p class="mb-4">
              The service only provides an interface to API-servers and is not responsible for their operation. The 
              service owner does not guarantee the accuracy or security of data obtained through third-party APIs.
            </p>
            
            <h2 class="text-h5 mb-3">Changes to the Terms</h2>
            <p class="mb-4">
              Terms of Service may change without prior notice. The user is responsible for checking for updates.
            </p>
          </v-card-text>
          
          <v-card-actions class="mt-4">
            <v-btn color="primary" to="/" variant="outlined">Return to Home</v-btn>
          </v-card-actions>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script setup lang="ts">
definePageMeta({
  title: 'Terms of Service',
  layout: 'default',
});
</script> 