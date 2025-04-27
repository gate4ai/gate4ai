#!/bin/bash

# Define the package to test
package_path="./..."  # Замените на фактический путь к вашему пакету

# Define the name of the test function
# Use ^ and $ for exact match, quote variable properly
test_name="^TestA2ATaskSend$" # Ensures only this test runs

# Define the maximum number of runs
max_runs=1000

# Counter for the number of runs
run_count=0
test_failed=0 # Flag to track if any test run failed

echo "Starting Go test loop for test '$test_name' (max $max_runs runs or until failure)..."

while [ "$run_count" -lt "$max_runs" ]; do
  run_count=$((run_count + 1))
  # Provide slightly better progress indication
  echo -n "Running test $run_count/$max_runs... " # -n keeps cursor on the same line

  # Run the specific Go test, capture output AND check its exit status
  # Capture combined stdout and stderr
  output=$(go test -count=1 -run="${test_name}" "${package_path}" -v 2>&1)
  test_status=$? # Capture the exit status of the go test command immediately

  # Check if the go test command itself failed (returned non-zero exit status)
  if [ $test_status -ne 0 ]; then
    # Test failed, print details and exit the loop
    echo "FAILED" # Complete the line started with echo -n
    echo ""
    echo "-----------------------------------------"
    echo "Test '${test_name}' FAILED on run: $run_count"
    echo "Exit Status: $test_status"
    echo "Failure Log:"
    echo "$output" # Print the captured output
    echo "-----------------------------------------"
    test_failed=1 # Mark that a failure occurred
    break # Exit the while loop immediately
  else
    # Test passed this run
    echo "Passed" # Complete the line started with echo -n
  fi

done # End of while loop

echo "" # Add a newline for separation

# Final status report based on the flag
if [ $test_failed -eq 1 ]; then
  echo "Test loop stopped due to failure."
  exit 1 # Exit script with a non-zero status to indicate failure
else
  echo "All $max_runs runs for test '${test_name}' passed successfully."
  exit 0 # Exit script successfully
fi