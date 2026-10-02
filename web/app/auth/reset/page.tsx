import IdCard from "../IdCard";
import ResetForm from "./ResetForm";

export default async function ResetPasswordPage({
  searchParams,
}: {
  searchParams: Promise<{ token?: string }>;
}) {
  const sp = await searchParams;
  return (
    <IdCard title="Choose a new password">
      <ResetForm token={sp.token ?? ""} />
    </IdCard>
  );
}
