/**
 * Better Auth drizzle schema — baseline `emailAndPassword` table set.
 * Lives in the dashboard's OWN `dashboard_auth` schema/db, isolated
 * from the gateway's `ai_gateway` (07-RESEARCH Pitfall 7).
 *
 * ============================================================
 * SCHEMA SOURCE-OF-TRUTH RULE — Phase 11 [reviews HIGH #3]
 * ============================================================
 *
 * The Better Auth CLI is the canonical schema source-of-truth for
 * two-factor tables:
 *
 *     bunx @better-auth/cli@latest migrate
 *
 * Do NOT add the following declarations to this file:
 *   - two-factor pgTable
 *   - two-factor-enabled column on the user table
 *   - backup-codes column on the two-factor table
 *   - two-factor-backup pgTable
 *
 * Downstream code that needs to reference these tables uses Better
 * Auth's runtime API (`auth.api.*` / `authClient.two-factor.*`), NOT
 * Drizzle direct queries. The CLI introspects the loaded plugins
 * (registered in `auth.ts`) and ALTERs the database accordingly —
 * ONE command, ONE source of truth, no Drizzle mirror.
 *
 * See `.planning/phases/11-prod-hardening/11-02-PLAN.md` acceptance
 * criteria (task 11-02-03) for the rationale.
 *
 * The baseline tables below (user / session / account / verification)
 * existed before Phase 11 and are kept here for Drizzle-side IntelliSense
 * on the parts of the dashboard that DO query the auth DB directly
 * (e.g. `app/settings/operadores/page.tsx`).
 */
import { boolean, pgTable, text, timestamp } from "drizzle-orm/pg-core";

export const user = pgTable("user", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  email: text("email").notNull().unique(),
  emailVerified: boolean("email_verified")
    .$defaultFn(() => false)
    .notNull(),
  image: text("image"),
  createdAt: timestamp("created_at", { withTimezone: true })
    .$defaultFn(() => new Date())
    .notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true })
    .$defaultFn(() => new Date())
    .notNull(),
});

export const session = pgTable("session", {
  id: text("id").primaryKey(),
  expiresAt: timestamp("expires_at", { withTimezone: true }).notNull(),
  token: text("token").notNull().unique(),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull(),
  ipAddress: text("ip_address"),
  userAgent: text("user_agent"),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
});

export const account = pgTable("account", {
  id: text("id").primaryKey(),
  accountId: text("account_id").notNull(),
  providerId: text("provider_id").notNull(),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  accessToken: text("access_token"),
  refreshToken: text("refresh_token"),
  idToken: text("id_token"),
  accessTokenExpiresAt: timestamp("access_token_expires_at", {
    withTimezone: true,
  }),
  refreshTokenExpiresAt: timestamp("refresh_token_expires_at", {
    withTimezone: true,
  }),
  scope: text("scope"),
  // emailAndPassword credential hash is stored here by Better Auth.
  password: text("password"),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull(),
});

export const verification = pgTable("verification", {
  id: text("id").primaryKey(),
  identifier: text("identifier").notNull(),
  value: text("value").notNull(),
  expiresAt: timestamp("expires_at", { withTimezone: true }).notNull(),
  createdAt: timestamp("created_at", { withTimezone: true }).$defaultFn(
    () => new Date(),
  ),
  updatedAt: timestamp("updated_at", { withTimezone: true }).$defaultFn(
    () => new Date(),
  ),
});
