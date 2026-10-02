"use client";

import { useState, useTransition } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { usd, type Category } from "@/lib/api";
import { createListingAction, suggestAction, uploadImageAction, type Suggestion } from "./actions";

type Fields = { title: string; description: string; category_id: string; price: string };
const EMPTY: Fields = { title: "", description: "", category_id: "", price: "" };

export default function SellForm({
  categories,
  hadError,
}: {
  categories: Category[];
  hadError: boolean;
}) {
  const [imageUrl, setImageUrl] = useState("");
  const [uploading, setUploading] = useState(false);
  const [fields, setFields] = useState<Fields>(EMPTY);
  // fields currently showing an unedited AI suggestion (the primary-colored edge cue)
  const [aiFields, setAiFields] = useState<Set<keyof Fields>>(new Set());
  const [suggestion, setSuggestion] = useState<Suggestion | null>(null);
  const [note, setNote] = useState("");
  const [pending, startTransition] = useTransition();

  async function handleFileSelect(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploading(true);
    setNote("");
    const data = new FormData();
    data.set("image", file);
    const result = await uploadImageAction(data);
    setUploading(false);
    if ("error" in result) {
      setNote(
        result.error === "signed_out"
          ? "Sign in to upload a photo."
          : "Couldn't upload that photo. JPEG and PNG only, 8MB or under.",
      );
      return;
    }
    setImageUrl(result.image_url);
  }

  function setField(name: keyof Fields, value: string) {
    setFields((f) => ({ ...f, [name]: value }));
    setAiFields((prev) => {
      if (!prev.has(name)) return prev;
      const next = new Set(prev);
      next.delete(name); // edited → no longer "AI suggested"
      return next;
    });
  }

  function requestSuggestion() {
    setNote("");
    startTransition(async () => {
      const result = await suggestAction(imageUrl, fields.title);
      if ("error" in result) {
        setNote(
          result.error === "signed_out"
            ? "Sign in to use suggestions."
            : "The assistant is unavailable right now. Fill the fields in yourself.",
        );
        return;
      }
      setSuggestion(result);
      const cat = categories.find((c) => c.slug === result.category_slug);
      // assist prices are cents; the field is dollars
      const mid =
        result.price_low != null && result.price_high != null
          ? ((result.price_low + result.price_high) / 200).toFixed(2)
          : "";
      setFields({
        title: result.title,
        description: result.description,
        category_id: cat ? String(cat.id) : "",
        price: mid,
      });
      setAiFields(new Set(["title", "description", "category_id", ...(mid ? ["price" as const] : [])]));
      setNote(
        result.model === "heuristic"
          ? "Drafted from your hint and recent sale prices. No vision model is configured."
          : `Suggested by ${result.model}. Edit anything before you list it.`,
      );
    });
  }

  // map form fields → assist's field names, keeping only unedited AI fields
  const acceptedFields = [...aiFields]
    .map((f) => (f === "category_id" ? "category" : f))
    .join(",");

  const ai = (name: keyof Fields) => (aiFields.has(name) ? "border-l-4 border-primary" : "");

  const categoryItems: { label: string; value: string | null }[] = [
    { label: "No category", value: null },
    ...categories.map((c) => ({ label: c.name, value: String(c.id) })),
  ];

  return (
    <form action={createListingAction} className="flex flex-col gap-3">
      {hadError ? (
        <p className="rounded-control bg-danger-tint px-3.5 py-2.5 text-sm text-danger">
          Could not create the listing. Check the fields and try again.
        </p>
      ) : null}
      <Input
        name="title"
        required
        maxLength={120}
        aria-label="Title"
        placeholder="Title"
        value={fields.title}
        onChange={(e) => setField("title", e.target.value)}
        className={ai("title")}
      />
      <input type="hidden" name="image_url" value={imageUrl} />
      <div className="flex gap-2">
        <Input
          type="file"
          accept="image/jpeg,image/png"
          onChange={handleFileSelect}
          disabled={uploading}
          aria-label="Photo"
          className="flex-1"
        />
        <Button
          type="button"
          variant="copilot"
          onClick={requestSuggestion}
          disabled={pending || uploading || (!imageUrl && !fields.title)}
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinejoin="round"
            aria-hidden="true"
            data-icon="inline-start"
            className={pending ? "animate-pulse" : ""}
          >
            <path d="M10.5 3.5 12.3 8.2 17 10l-4.7 1.8-1.8 4.7-1.8-4.7L4 10l4.7-1.8z" />
            <path d="M17.5 14.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z" />
          </svg>
          {pending ? "Suggesting…" : "Suggest"}
        </Button>
      </div>
      <p aria-live="polite" className="text-xs leading-5 text-faint empty:hidden">
        {uploading ? "Uploading…" : note}
      </p>
      {imageUrl ? (
        <img
          src={imageUrl}
          alt="Listing photo preview"
          className="max-h-48 rounded-card object-cover"
        />
      ) : null}
      <Textarea
        name="description"
        rows={4}
        aria-label="Description"
        placeholder="Description: condition, age, what's included"
        value={fields.description}
        onChange={(e) => setField("description", e.target.value)}
        className={ai("description")}
      />
      <div className="flex gap-3">
        <InputGroup className={`flex-1 ${ai("price")}`}>
          <InputGroupAddon>$</InputGroupAddon>
          <InputGroupInput
            name="price"
            type="number"
            required
            min={0.01}
            step={0.01}
            placeholder="Price (USD)"
            value={fields.price}
            onChange={(e) => setField("price", e.target.value)}
            className="money"
          />
        </InputGroup>
        <Select
          name="category_id"
          items={categoryItems}
          value={fields.category_id || null}
          onValueChange={(v) => setField("category_id", v ?? "")}
        >
          <SelectTrigger aria-label="Category" className={`w-full flex-1 ${ai("category_id")}`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {categoryItems.map((item) => (
                <SelectItem key={item.value ?? "none"} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      {suggestion?.price_low != null && suggestion?.price_high != null ? (
        <p className="money text-xs text-faint">
          Similar items sold for {usd(suggestion.price_low)} to {usd(suggestion.price_high)}
        </p>
      ) : null}

      <input type="hidden" name="suggestion_id" value={suggestion?.suggestion_id ?? ""} />
      <input type="hidden" name="accepted_fields" value={suggestion ? acceptedFields : ""} />
      <Button type="submit" className="mt-1">
        List it
      </Button>
    </form>
  );
}
