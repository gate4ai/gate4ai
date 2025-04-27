<template>
  <div>
    <h1 class="text-h3 mb-6">User Management</h1>

    <!-- Search and Filter -->
    <v-row class="mb-6">
      <v-col cols="12" md="12">
        <v-text-field
          v-model="searchQuery"
          label="Search users by name, email, or company"
          prepend-inner-icon="mdi-magnify"
          variant="outlined"
          hide-details
          @update:model-value="searchUsers"
        />
      </v-col>
    </v-row>

    <!-- Loading State -->
    <div v-if="isLoading" class="d-flex justify-center py-12">
      <v-progress-circular indeterminate color="primary" />
    </div>

    <!-- Users Table -->
    <v-table v-else>
      <thead>
        <tr>
          <th class="text-left">Full Name</th>
          <th class="text-left">Email</th>
          <th class="text-left">Company</th>
          <th class="text-left">Role</th>
          <th class="text-left">Status</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="user in filteredUsers"
          :key="user.id"
          class="user-row"
          @click="viewUserProfile(user.id)"
        >
          <td>{{ user.name || "-" }}</td>
          <td>
            <a :href="`mailto:${user.email}`">{{ user.email }}</a>
          </td>
          <td>{{ user.company || "-" }}</td>
          <td>{{ formatRole(user.role) }}</td>
          <td>{{ formatStatus(user.status) }}</td>
        </tr>
      </tbody>
    </v-table>

    <!-- Empty State -->
    <v-row v-if="!isLoading && filteredUsers.length === 0">
      <v-col cols="12" class="text-center py-12">
        <v-icon size="x-large" color="grey">mdi-account-off</v-icon>
        <h3 class="text-h5 mt-4 mb-2">No users found</h3>
        <p class="text-body-1">Try adjusting your search</p>
      </v-col>
    </v-row>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from "vue";

definePageMeta({
  middleware: ["auth"],
  title: "User Management",
});

interface User {
  id: string;
  name?: string;
  email: string;
  company?: string;
  role?: string;
  status?: string;
  rbac?: string;
}

const users = ref<User[]>([]);
const searchQuery = ref("");
const isLoading = ref(true);

// Fetch users on component mount
onMounted(async () => {
  await fetchUsers();
});

async function fetchUsers() {
  isLoading.value = true;

  try {
    const { $api } = useNuxtApp();
    users.value = await $api.getJson("/users");
  } catch (error) {
    console.error("Error fetching users:", error);
    // Show error message to user
  } finally {
    isLoading.value = false;
  }
}

const filteredUsers = computed(() => {
  let result = [...users.value];

  // Apply search filter
  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase();
    result = result.filter(
      (user) =>
        (user.name || "").toLowerCase().includes(query) ||
        user.email.toLowerCase().includes(query) ||
        (user.company || "").toLowerCase().includes(query)
    );
  }
  return result;
});

function searchUsers() {
  // Client-side filtering is handled by the computed property
  console.log("Searching for:", searchQuery.value);
}

function viewUserProfile(userId: string) {
  navigateTo(`/users/${userId}`);
}

function formatRole(role?: string) {
  switch (role) {
    case "ADMIN":
      return "Admin";
    case "SECURITY":
      return "Security";
    case "EMPTY":
    default:
      return "-";
  }
}

function formatStatus(status?: string) {
  switch (status) {
    case "ACTIVE":
      return "Active";
    case "BLOCKED":
      return "Blocked";
    case "EMAIL_NOT_CONFIRMED":
      return "Email not confirmed";
    default:
      return "-";
  }
}
</script>

<style scoped>
.user-row {
  cursor: pointer;
}
.user-row:hover {
  background-color: var(--v-hover-color, rgba(0, 0, 0, 0.04));
}
</style>
