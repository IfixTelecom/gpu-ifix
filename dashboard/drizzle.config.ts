import { defineConfig } from "drizzle-kit";

if (!process.env.DASHBOARD_DATABASE_URL) {
  throw new Error("DASHBOARD_DATABASE_URL must be set for drizzle-kit");
}

export default defineConfig({
  schema: "./src/lib/schema.ts",
  dialect: "postgresql",
  dbCredentials: {
    url: process.env.DASHBOARD_DATABASE_URL,
    ssl: { rejectUnauthorized: false },
  },
  verbose: true,
  strict: true,
});
