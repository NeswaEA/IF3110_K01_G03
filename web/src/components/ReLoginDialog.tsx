import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { ApiError } from "@/api/client";
import { login, meQuery } from "@/api/queries/auth";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * Shown when the session ends while the page holds unsaved work: the user
 * signs in again without leaving it. Nothing that failed is resent; the user
 * repeats the action.
 */
export function ReLoginDialog({ open, onSignedIn, onLeave }: { open: boolean; onSignedIn: () => void; onLeave: () => void }) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setPending(true);
    setError(null);
    try {
      const user = await login(String(form.get("username")), String(form.get("password")));
      queryClient.setQueryData(meQuery.queryKey, user);
      onSignedIn();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Login gagal. Coba lagi.");
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open}>
      <DialogContent showCloseButton={false} onEscapeKeyDown={(e) => e.preventDefault()} onInteractOutside={(e) => e.preventDefault()}>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Sesi berakhir</DialogTitle>
            <DialogDescription>
              Login lagi untuk melanjutkan. Perubahan di halaman ini tetap ada; aksi yang gagal perlu diulang.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="relogin-username">Username</Label>
            <Input id="relogin-username" name="username" autoComplete="username" required />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="relogin-password">Password</Label>
            <Input id="relogin-password" name="password" type="password" autoComplete="current-password" required />
          </div>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onLeave} disabled={pending}>
              Ke halaman login
            </Button>
            <Button type="submit" disabled={pending}>
              Login
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
