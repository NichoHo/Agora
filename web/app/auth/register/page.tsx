import IdCard from "../IdCard";
import RegisterForm from "./RegisterForm";
import { safePath } from "@/lib/auth";

export default async function RegisterPage({
  searchParams,
}: {
  searchParams: Promise<{ return_to?: string }>;
}) {
  const sp = await searchParams;
  return (
    <IdCard title="Create your Agora ID">
      <RegisterForm returnTo={safePath(sp.return_to)} />
    </IdCard>
  );
}
