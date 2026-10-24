FROM node:24.22.0@sha256:14078204b6bb268c107ceff6d3c43f45a888105e5c2b18b14c32b3409d93a1b0
WORKDIR /e2e
COPY tests/e2e/package.json tests/e2e/package-lock.json ./
RUN npm ci && npx playwright install --with-deps --only-shell chromium \
 && rm -rf /var/lib/apt/lists/* /root/.npm
COPY tests/e2e/ ./
RUN node --test check-results.test.mjs
CMD ["npm", "test"]
