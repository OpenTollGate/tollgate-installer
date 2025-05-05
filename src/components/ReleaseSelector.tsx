import React, { useState } from 'react';
import styled from 'styled-components';
import Button from './common/Button';
import { NDKEvent } from '@nostr-dev-kit/ndk';
import {
  isReleaseCompatible,
  getReleaseVersion,
  getReleaseDate,
  getReleaseModel,
  getReleaseArchitecture,
  getReleaseOpenWrtVersion
} from '../utils/releaseUtils';

interface ReleaseSelectorProps {
  releases: NDKEvent[];
  routerBoardName?: string;
  selectedReleaseId?: string;
  onReleaseSelect: (release: NDKEvent) => void;
  buttonLabel?: string;
  disabled?: boolean;
}

// Styled components
const ReleaseSelectorContainer = styled.div`
  position: relative;
  z-index: 10; /* Ensure dropdown appears above other content */
`;

const ReleaseSelectorButton = styled(Button)`
  width: 100%;
  justify-content: space-between;
  padding: 0.75rem 1rem;
`;

const ReleaseList = styled.ul`
  position: absolute;
  top: 100%;
  right: 0; /* Align to the right by default */
  width: 450px; /* Even wider fixed width */
  min-width: 100%; /* Ensure it's at least as wide as the button */
  background: white;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  list-style: none;
  padding: 0;
  margin: 0;
  z-index: 20;
  max-height: 300px; /* Increased height */
  overflow-y: auto;
  box-shadow: 0 4px 8px rgba(0, 0, 0, 0.1); /* Add shadow for better visibility */
`;

interface ReleaseItemProps {
  $isSelected: boolean;
  $isCompatible: boolean;
}

const ReleaseItem = styled.li<ReleaseItemProps>`
  padding: 1rem 1.25rem;
  cursor: pointer;
  background-color: ${props => props.$isSelected ? props.theme.colors.primaryLight : 'transparent'};
  color: ${props => props.$isCompatible ? props.theme.colors.text : props.theme.colors.textSecondary};
  opacity: ${props => props.$isCompatible ? 1 : 0.6};
  border-bottom: 1px solid ${props => props.theme.colors.border};
  
  &:last-child {
    border-bottom: none;
  }
  
  &:hover {
    background-color: ${props => props.theme.colors.primaryLight};
  }
`;

const ReleaseHeader = styled.div`
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
`;

const ReleaseName = styled.div`
  font-size: ${props => props.theme.fontSizes.md};
  font-weight: ${props => props.theme.fontWeights.medium};
`;

const ReleaseDate = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
`;

const ReleaseDetails = styled.div`
  display: flex;
  justify-content: space-between;
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  margin-top: 0.25rem;
`;

const ReleaseModelInfo = styled.div``;

const ReleaseTechInfo = styled.div`
  text-align: right;
`;

const ReleaseItemContent = styled.div`
  display: flex;
  flex-direction: column;
  width: 100%;
`;

// Component
const ReleaseSelector: React.FC<ReleaseSelectorProps> = ({
  releases,
  routerBoardName,
  selectedReleaseId,
  onReleaseSelect,
  buttonLabel = 'Select Release',
  disabled = false
}) => {
  const [showDropdown, setShowDropdown] = useState(false);
  
  // Find the selected release from the ID
  const selectedRelease = releases.find(r => r.id === selectedReleaseId);
  
  // Group releases by compatibility
  const compatibleReleases = releases.filter(r => isReleaseCompatible(r, routerBoardName));
  const incompatibleReleases = releases.filter(r => !isReleaseCompatible(r, routerBoardName));
  
  // Generate button label
  const displayLabel = selectedRelease 
    ? `Release ${getReleaseVersion(selectedRelease)} ▼`
    : `${buttonLabel} ▼`;
  
  const toggleDropdown = () => {
    if (disabled) return;
    setShowDropdown(!showDropdown);
  };
  
  const handleReleaseSelect = (release: NDKEvent) => {
    onReleaseSelect(release);
    setShowDropdown(false);
  };
  
  return (
    <ReleaseSelectorContainer>
      <ReleaseSelectorButton
        variant="outline"
        size="small"
        onClick={toggleDropdown}
        disabled={disabled}
      >
        {displayLabel}
      </ReleaseSelectorButton>
      
      {showDropdown && (
        <ReleaseList>
          {/* Render compatible releases first */}
          {compatibleReleases.map((release) => (
            <ReleaseItem
              key={release.id}
              onClick={() => handleReleaseSelect(release)}
              $isSelected={selectedReleaseId === release.id && release.id !== ""}
              $isCompatible={true}
            >
              <ReleaseItemContent>
                <ReleaseHeader>
                  <ReleaseName>
                    {getReleaseVersion(release)} (Compatible)
                  </ReleaseName>
                  <ReleaseDate>
                    {getReleaseDate(release)}
                  </ReleaseDate>
                </ReleaseHeader>
                <ReleaseDetails>
                  <ReleaseModelInfo>
                    {getReleaseModel(release)}
                  </ReleaseModelInfo>
                  <ReleaseTechInfo>
                    {getReleaseArchitecture(release)} • OpenWRT {getReleaseOpenWrtVersion(release)}
                  </ReleaseTechInfo>
                </ReleaseDetails>
              </ReleaseItemContent>
            </ReleaseItem>
          ))}
          
          {/* Then render incompatible releases */}
          {incompatibleReleases.map((release) => (
            <ReleaseItem
              key={release.id}
              onClick={() => handleReleaseSelect(release)}
              $isSelected={selectedReleaseId === release.id && release.id !== ""}
              $isCompatible={false}
            >
              <ReleaseItemContent>
                <ReleaseHeader>
                  <ReleaseName>
                    {getReleaseVersion(release)} (Incompatible)
                  </ReleaseName>
                  <ReleaseDate>
                    {getReleaseDate(release)}
                  </ReleaseDate>
                </ReleaseHeader>
                <ReleaseDetails>
                  <ReleaseModelInfo>
                    {getReleaseModel(release)}
                  </ReleaseModelInfo>
                  <ReleaseTechInfo>
                    {getReleaseArchitecture(release)} • OpenWRT {getReleaseOpenWrtVersion(release)}
                  </ReleaseTechInfo>
                </ReleaseDetails>
              </ReleaseItemContent>
            </ReleaseItem>
          ))}
        </ReleaseList>
      )}
    </ReleaseSelectorContainer>
  );
};

export default ReleaseSelector;