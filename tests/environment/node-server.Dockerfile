FROM node:lts-alpine

WORKDIR /app

# Install the required packages
RUN npm install -g supergateway @modelcontextprotocol/server-everything

# Set the environment variables
ENV PORT=8000

# Expose the port
EXPOSE ${PORT}

# Start the server
CMD ["sh", "-c", "npx -y supergateway --port ${PORT} --stdio 'npx -y @modelcontextprotocol/server-everything'"]