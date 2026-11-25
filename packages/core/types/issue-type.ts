import type { IssuePropertyValue } from "./property";

export interface IssueTypeField {
  property_id: string;
  required: boolean;
  default_value?: IssuePropertyValue;
}

export interface IssueType {
  id: string;
  workspace_id: string;
  name: string;
  fields: IssueTypeField[];
  position: number;
  archived: boolean;
  archived_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateIssueTypeRequest {
  name: string;
  fields: IssueTypeField[];
}

export interface UpdateIssueTypeRequest {
  name?: string;
  fields?: IssueTypeField[];
  archived?: boolean;
}

export interface ListIssueTypesResponse {
  issue_types: IssueType[];
  total: number;
}
